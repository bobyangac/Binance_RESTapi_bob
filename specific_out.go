package bnnapi

import "github.com/shopspring/decimal"

// format: [][]string{asset, available, total}
func (c *Client) GetBalances() ([][]string, bool) {
	res, err := c.spotUser.AccountData()
	if err != nil {
		return [][]string{}, false
	}
	var result [][]string
	for _, item := range res.Balances {
		free, _ := decimal.NewFromString(item.Free)
		lock, _ := decimal.NewFromString(item.Locked)
		total := free.Add(lock)
		data := []string{item.Asset, free.String(), total.String()}
		result = append(result, data)
	}
	return result, true
}

// format: [][]string{oid, symbol, product, subaccount, price, qty, side, execType, UnfilledQty}
func (c *Client) GetOpenOrders() ([][]string, bool) {
	res, err := c.GetCurrentSpotOrders("")
	if err != nil {
		return [][]string{}, false
	}
	var result [][]string
	for _, item := range res {
		oid := decimal.NewFromInt(int64(item.Orderid)).String()
		origQty, _ := decimal.NewFromString(item.Origqty)
		filledQty, _ := decimal.NewFromString(item.Executedqty)
		unfilledQty := origQty.Sub(filledQty).String()
		data := []string{oid, item.Symbol, "spot", c.subaccount, item.Price, item.Origqty, item.Side, item.Type, unfilledQty}
		result = append(result, data)
	}
	return result, true
}

// [][]string{oid, symbol, product, subaccount, price, qty, side, execType, fee, filledQty, timestamp}
func (c *Client) GetTradeReports() ([][]string, bool) {
	var result [][]string
	for {
		trade, err := c.spotUser.ReadTrade()
		if err != nil {
			break
		}
		var execType string
		if trade.IsMaker {
			execType = "maker"
		} else {
			execType = "taker"
		}
		st := decimal.NewFromInt(trade.TimeStamp.Unix()).String()
		data := []string{trade.Oid, trade.Symbol, "spot", c.subaccount, trade.Price.String(), trade.Qty.String(), trade.Side, execType, trade.Fee.String(), trade.Qty.String(), st}
		result = append(result, data)
	}
	if len(result) == 0 {
		return result, false
	}
	return result, true
}
