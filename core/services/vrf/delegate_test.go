package vrf_test

import (
	"context"
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"github.com/smartcontractkit/chainlink/core/services/pipeline"
	"github.com/smartcontractkit/chainlink/core/store/models"
	"github.com/stretchr/testify/assert"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/smartcontractkit/chainlink/core/internal/cltest"
	eth_mocks "github.com/smartcontractkit/chainlink/core/services/eth/mocks"
	"github.com/smartcontractkit/chainlink/core/services/log"
	log_mocks "github.com/smartcontractkit/chainlink/core/services/log/mocks"
	"github.com/smartcontractkit/chainlink/core/services/signatures/secp256k1"
	"github.com/smartcontractkit/chainlink/core/services/vrf"
	"github.com/smartcontractkit/chainlink/core/store"
	"github.com/smartcontractkit/chainlink/core/testdata/testspecs"
	"github.com/smartcontractkit/chainlink/core/utils"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type vrfUniverse struct {
	jpv2      cltest.JobPipelineV2TestHelper
	lb        *log_mocks.Broadcaster
	ec        *eth_mocks.Client
	vorm      vrf.ORM
	ks        *vrf.VRFKeyStore
	vrfkey    secp256k1.PublicKey
	submitter common.Address
}

func setup(t *testing.T, db *gorm.DB, cfg *cltest.TestConfig, s store.KeyStoreInterface) vrfUniverse {
	// Mock all chain interactions
	lb := new(log_mocks.Broadcaster)
	ec := new(eth_mocks.Client)

	// Don't mock db interactions
	jpv2 := cltest.NewJobPipelineV2(t, cfg, db)
	vorm := vrf.NewORM(db)
	ks := vrf.NewVRFKeyStore(vorm, utils.FastScryptParams)
	require.NoError(t, s.Unlock(cltest.Password))
	_, err := s.CreateNewKey()
	require.NoError(t, err)
	submitter, err := s.GetRoundRobinAddress()
	require.NoError(t, err)
	vrfkey, err := ks.CreateKey("blah")
	require.NoError(t, err)
	_, err = ks.Unlock("blah")
	require.NoError(t, err)

	return vrfUniverse{
		jpv2:      jpv2,
		lb:        lb,
		ec:        ec,
		vorm:      vorm,
		ks:        ks,
		vrfkey:    vrfkey,
		submitter: submitter,
	}
}

func (v vrfUniverse) Assert(t *testing.T) {
	v.lb.AssertExpectations(t)
	v.ec.AssertExpectations(t)
}

func TestDelegate(t *testing.T) {
	cfg, orm, cleanupDB := cltest.BootstrapThrowawayORM(t, "vrf_delegate", true)
	defer cleanupDB()
	store, cleanup := cltest.NewStoreWithConfig(t, cfg)
	defer cleanup()
	vuni := setup(t, orm.DB, cfg, store.KeyStore)

	vd := vrf.NewDelegate(orm.DB,
		vuni.vorm,
		store.KeyStore,
		vuni.ks,
		vuni.jpv2.Pr,
		vuni.jpv2.Prm,
		vuni.lb,
		vuni.ec,
		vrf.NewConfig(0, utils.FastScryptParams, 1000, 10))
	vs := testspecs.GenerateVRFSpec(testspecs.VRFSpecParams{PublicKey: vuni.vrfkey.String()})
	t.Log(vs)
	jb, err := vrf.ValidateVRFSpec(vs.Toml())
	require.NoError(t, err)
	require.NoError(t, vuni.jpv2.Jrm.CreateJob(context.Background(), &jb, *pipeline.NewTaskDAG()))
	vl, err := vd.ServicesForSpec(jb)
	require.NoError(t, err)
	require.Len(t, vl, 1)

	listener := vl[0]
	done := make(chan struct{})
	unsubscribe := func() { done <- struct{}{} }

	var logListener log.Listener
	vuni.lb.On("Register", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		logListener = args.Get(0).(log.Listener)
	}).Return(unsubscribe)
	require.NoError(t, listener.Start())

	t.Run("valid log", func(t *testing.T) {
		vuni.lb.On("WasAlreadyConsumed", mock.Anything, mock.Anything).Return(false, nil)
		vuni.lb.On("MarkConsumed", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			done <- struct{}{}
		}).Return(nil)

		// Send a valid log
		pk, err := secp256k1.NewPublicKeyFromHex(vs.PublicKey)
		require.NoError(t, err)
		reqID := cltest.NewHash()
		logListener.HandleLog(log.NewLogBroadcast(types.Log{
			// Data has all the NON-indexed parameters
			Data: append(append(append(append(
				pk.MustHash().Bytes(),                        // key hash
				common.BigToHash(big.NewInt(42)).Bytes()...), // seed
				cltest.NewHash().Bytes()...), // sender
				cltest.NewHash().Bytes()...), // fee
				reqID.Bytes()...), // requestID
			// JobID is indexed, thats why it lives in the Topics.
			Topics:      []common.Hash{{}, jb.ExternalIDToTopicHash()}, // jobID
			Address:     common.Address{},
			BlockNumber: 0,
			TxHash:      common.Hash{},
			TxIndex:     0,
			BlockHash:   common.Hash{},
			Index:       0,
			Removed:     false,
		}))
		select {
		case <-time.After(1 * time.Second):
			t.Errorf("failed to consume log")
		case <-done:
		}

		// Ensure we created a successful run.
		runs, err := vuni.jpv2.Prm.GetAllRuns()
		require.NoError(t, err)
		require.Len(t, runs, 1)
		assert.False(t, runs[0].Errors.HasError())
		m, ok := runs[0].Meta.Val.(map[string]interface{})
		require.True(t, ok)
		_, ok = m["eth_tx_id"]
		assert.True(t, ok)
		assert.Len(t, runs[0].PipelineTaskRuns, 0)

		// Ensure we have queued up a valid eth transaction
		// Linked to  requestID
		var ethTxes []models.EthTx
		err = orm.DB.Find(&ethTxes).Error
		require.NoError(t, err)
		require.Len(t, ethTxes, 1)
		assert.Equal(t, vs.CoordinatorAddress, ethTxes[0].ToAddress.String())
		var em models.EthTxMetaV2
		err = json.Unmarshal(ethTxes[0].Meta.RawMessage, &em)
		require.NoError(t, err)
		assert.Equal(t, reqID, em.RequestID)
		require.NoError(t, orm.DB.Exec(`TRUNCATE eth_txes,pipeline_runs CASCADE`).Error)
	})

	t.Run("invalid log", func(t *testing.T) {
		vuni.lb.On("WasAlreadyConsumed", mock.Anything, mock.Anything).Return(false, nil)
		vuni.lb.On("MarkConsumed", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
			done <- struct{}{}
		}).Return(nil)
		// Send a invalid log (keyhash doesnt match)
		logListener.HandleLog(log.NewLogBroadcast(types.Log{
			// Data has all the NON-indexed parameters
			Data: append(append(append(append(
				cltest.NewHash().Bytes(),                     // key hash
				common.BigToHash(big.NewInt(42)).Bytes()...), // seed
				cltest.NewHash().Bytes()...), // sender
				cltest.NewHash().Bytes()...), // fee
				cltest.NewHash().Bytes()...), // requestID
			// JobID is indexed, thats why it lives in the Topics.
			Topics:      []common.Hash{{}, jb.ExternalIDToTopicHash()}, // jobID
			Address:     common.Address{},
			BlockNumber: 0,
			TxHash:      common.Hash{},
			TxIndex:     0,
			BlockHash:   common.Hash{},
			Index:       0,
			Removed:     false,
		}))
		select {
		case <-time.After(1 * time.Second):
			t.Errorf("failed to consume log")
		case <-done:
		}

		// Ensure we have not created a run.
		runs, err := vuni.jpv2.Prm.GetAllRuns()
		require.NoError(t, err)
		require.Equal(t, len(runs), 0)

		// Ensure we have NOT queued up an eth transaction
		var ethTxes []models.EthTx
		err = orm.DB.Find(&ethTxes).Error
		require.NoError(t, err)
		require.Len(t, ethTxes, 0)
	})

	require.NoError(t, listener.Close())
	select {
	case <-time.After(1 * time.Second):
		t.Errorf("failed to unsubscribe")
	case <-done:
	}

	vuni.Assert(t)
}