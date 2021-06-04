// SPDX-License-Identifier: MIT
pragma solidity ^0.7.0;

import "../AggregatorV2V3Interface.sol";

/**
 * @dev Test consuming AggregatorV2V3Interface using Solidity version 0.7.x
 */
contract AggregatorInterfaceConsumerTest7 {

  AggregatorV2V3Interface public s_priceFeed;

  /**
   * @param priceFeed AggregatorV2V3Interface
   */
  constructor(
    AggregatorV2V3Interface priceFeed
  ) {
    s_priceFeed = priceFeed;
  }

  /**
   * @notice Get the latest price from the price feed
   * @return price int256
   */
  function getLatestPrice()
    public
    view
    returns(
      int256
    )
  {
    return s_priceFeed.latestAnswer();
  }
}