package bnnapi

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/shopspring/decimal"
	log "github.com/sirupsen/logrus"
)

type OrderBookBranch struct {
	bids          bookBranch
	asks          bookBranch
	lastUpdatedId decimal.Decimal
	snapShoted    bool
	cancel        *context.CancelFunc
	buyTrade      tradeImpact
	sellTrade     tradeImpact
	LookBack      time.Duration
	fromLevel     int
	toLevel       int
}

type tradeImpact struct {
	mux      sync.RWMutex
	Stamp    []time.Time
	Qty      []decimal.Decimal
	Notional []decimal.Decimal
}

type bookBranch struct {
	mux   sync.RWMutex
	Book  [][]string
	Micro []bookMicro
}

type bookMicro struct {
	OrderNum int
	Trend    string
}

type wS struct {
	Channel       string
	OnErr         bool
	Logger        *log.Logger
	Conn          *websocket.Conn
	LastUpdatedId decimal.Decimal
}

// logurs as log system
func (o *OrderBookBranch) GetOrderBookSnapShot(product, symbol string) error {
	client := New("", "", "")
	switch product {
	case "spot":
		res, err := client.SpotDepth(symbol, 5000)
		if err != nil {
			return err
		}
		o.bids.mux.Lock()
		o.bids.Book = res.Bids
		for i := 0; i < len(res.Bids); i++ {
			// micro part
			micro := bookMicro{
				OrderNum: 1, // initial order num is 1
			}
			o.bids.Micro = append(o.bids.Micro, micro)
		}
		o.bids.mux.Unlock()
		o.asks.mux.Lock()
		o.asks.Book = res.Asks
		for i := 0; i < len(res.Asks); i++ {
			// micro part
			micro := bookMicro{
				OrderNum: 1, // initial order num is 1
			}
			o.asks.Micro = append(o.asks.Micro, micro)
		}
		o.asks.mux.Unlock()
		o.lastUpdatedId = decimal.NewFromInt(int64(res.LastUpdateID))
	case "swap":
		res, err := client.SwapDepth(symbol, 1000)
		if err != nil {
			return err
		}
		o.bids.mux.Lock()
		o.bids.Book = res.Bids
		for i := 0; i < len(res.Bids); i++ {
			// micro part
			micro := bookMicro{
				OrderNum: 1, // initial order num is 1
			}
			o.bids.Micro = append(o.bids.Micro, micro)
		}
		o.bids.mux.Unlock()
		o.asks.mux.Lock()
		o.asks.Book = res.Asks
		for i := 0; i < len(res.Asks); i++ {
			// micro part
			micro := bookMicro{
				OrderNum: 1, // initial order num is 1
			}
			o.asks.Micro = append(o.asks.Micro, micro)
		}
		o.asks.mux.Unlock()
		o.lastUpdatedId = decimal.NewFromInt(int64(res.LastUpdateID))
	}
	o.snapShoted = true
	return nil
}

func (o *OrderBookBranch) UpdateNewComing(message *map[string]interface{}) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		// bid
		bids, ok := (*message)["b"].([]interface{})
		if !ok {
			return
		}
		for _, bid := range bids {
			price, _ := decimal.NewFromString(bid.([]interface{})[0].(string))
			qty, _ := decimal.NewFromString(bid.([]interface{})[1].(string))
			o.DealWithBidPriceLevel(price, qty)
		}
	}()
	go func() {
		defer wg.Done()
		// ask
		asks, ok := (*message)["a"].([]interface{})
		if !ok {
			return
		}
		for _, ask := range asks {
			price, _ := decimal.NewFromString(ask.([]interface{})[0].(string))
			qty, _ := decimal.NewFromString(ask.([]interface{})[1].(string))
			o.DealWithAskPriceLevel(price, qty)
		}
	}()
	wg.Wait()
}

func (o *OrderBookBranch) DealWithBidPriceLevel(price, qty decimal.Decimal) {
	o.bids.mux.Lock()
	defer o.bids.mux.Unlock()
	l := len(o.bids.Book)
	for level, item := range o.bids.Book {
		bookPrice, _ := decimal.NewFromString(item[0])
		switch {
		case price.GreaterThan(bookPrice):
			// insert level
			if qty.IsZero() {
				// ignore
				return
			}
			o.bids.Book = append(o.bids.Book, []string{})
			copy(o.bids.Book[level+1:], o.bids.Book[level:])
			// micro part
			o.bids.Micro = append(o.bids.Micro, bookMicro{})
			copy(o.bids.Micro[level+1:], o.bids.Micro[level:])
			o.bids.Book[level] = []string{price.String(), qty.String()}
			o.bids.Micro[level].OrderNum = 1
			return
		case price.LessThan(bookPrice):
			if level == l-1 {
				// insert last level
				if qty.IsZero() {
					// ignore
					return
				}
				o.bids.Book = append(o.bids.Book, []string{price.String(), qty.String()})
				data := bookMicro{
					OrderNum: 1,
				}
				o.bids.Micro = append(o.bids.Micro, data)
				return
			}
			continue
		case price.Equal(bookPrice):
			if qty.IsZero() {
				// delete level
				switch {
				case level == l-1:
					o.bids.Book = o.bids.Book[:l-1]
					o.bids.Micro = o.bids.Micro[:l-1]
				default:
					o.bids.Book = append(o.bids.Book[:level], o.bids.Book[level+1:]...)
					o.bids.Micro = append(o.bids.Micro[:level], o.bids.Micro[level+1:]...)
				}
				return
			}
			oldQty, _ := decimal.NewFromString(o.bids.Book[level][1])
			switch {
			case oldQty.GreaterThan(qty):
				// add order
				o.bids.Micro[level].OrderNum++
				o.bids.Micro[level].Trend = "add"
			case oldQty.LessThan(qty):
				// cut order
				o.bids.Micro[level].OrderNum--
				o.bids.Micro[level].Trend = "cut"
				if o.bids.Micro[level].OrderNum < 1 {
					o.bids.Micro[level].OrderNum = 1
				}
			}
			o.bids.Book[level][1] = qty.String()
			return
		}
	}
}

func (o *OrderBookBranch) DealWithAskPriceLevel(price, qty decimal.Decimal) {
	o.asks.mux.Lock()
	defer o.asks.mux.Unlock()
	l := len(o.asks.Book)
	for level, item := range o.asks.Book {
		bookPrice, _ := decimal.NewFromString(item[0])
		switch {
		case price.LessThan(bookPrice):
			// insert level
			if qty.IsZero() {
				// ignore
				return
			}
			o.asks.Book = append(o.asks.Book, []string{})
			copy(o.asks.Book[level+1:], o.asks.Book[level:])
			// micro part
			o.asks.Micro = append(o.asks.Micro, bookMicro{})
			copy(o.asks.Micro[level+1:], o.asks.Micro[level:])
			o.asks.Book[level] = []string{price.String(), qty.String()}
			o.asks.Micro[level].OrderNum = 1
			return
		case price.GreaterThan(bookPrice):
			if level == l-1 {
				// insert last level
				if qty.IsZero() {
					// ignore
					return
				}
				o.asks.Book = append(o.asks.Book, []string{price.String(), qty.String()})
				data := bookMicro{
					OrderNum: 1,
				}
				o.asks.Micro = append(o.asks.Micro, data)
				return
			}
			continue
		case price.Equal(bookPrice):
			if qty.IsZero() {
				// delete level
				switch {
				case level == l-1:
					o.asks.Book = o.asks.Book[:l-1]
					o.asks.Micro = o.asks.Micro[:l-1]
				default:
					o.asks.Book = append(o.asks.Book[:level], o.asks.Book[level+1:]...)
					o.asks.Micro = append(o.asks.Micro[:level], o.asks.Micro[level+1:]...)
				}
				return
			}
			oldQty, _ := decimal.NewFromString(o.asks.Book[level][1])
			switch {
			case oldQty.GreaterThan(qty):
				// add order
				o.asks.Micro[level].OrderNum++
				o.asks.Micro[level].Trend = "add"
			case oldQty.LessThan(qty):
				// cut order
				o.asks.Micro[level].OrderNum--
				o.asks.Micro[level].Trend = "cut"
				if o.asks.Micro[level].OrderNum < 1 {
					o.asks.Micro[level].OrderNum = 1
				}
			}
			o.asks.Book[level][1] = qty.String()
			return
		}
	}
}

func (o *OrderBookBranch) Close() {
	(*o.cancel)()
	o.snapShoted = false
	o.bids.mux.Lock()
	o.bids.Book = [][]string{}
	o.bids.mux.Unlock()
	o.asks.mux.Lock()
	o.asks.Book = [][]string{}
	o.asks.mux.Unlock()
}

// return bids, ready or not
func (o *OrderBookBranch) GetBids() ([][]string, bool) {
	o.bids.mux.RLock()
	defer o.bids.mux.RUnlock()
	if len(o.bids.Book) == 0 || !o.snapShoted {
		return [][]string{}, false
	}
	book := o.bids.Book
	return book, true
}

func (o *OrderBookBranch) GetBidsEnoughForValue(value decimal.Decimal) ([][]string, bool) {
	o.bids.mux.RLock()
	defer o.bids.mux.RUnlock()
	if len(o.bids.Book) == 0 || !o.snapShoted {
		return [][]string{}, false
	}
	var loc int
	var sumValue decimal.Decimal
	for level, data := range o.bids.Book {
		if len(data) != 2 {
			return [][]string{}, false
		}
		price, _ := decimal.NewFromString(data[0])
		size, _ := decimal.NewFromString(data[1])
		sumValue = sumValue.Add(price.Mul(size))
		if sumValue.GreaterThan(value) {
			loc = level
			break
		}
	}
	book := o.bids.Book[:loc+1]
	return book, true
}

func (o *OrderBookBranch) GetBidMicro(idx int) (*bookMicro, bool) {
	o.bids.mux.RLock()
	defer o.bids.mux.RUnlock()
	if len(o.bids.Book) == 0 || !o.snapShoted {
		return nil, false
	}
	micro := o.bids.Micro[idx]
	return &micro, true
}

// return asks, ready or not
func (o *OrderBookBranch) GetAsks() ([][]string, bool) {
	o.asks.mux.RLock()
	defer o.asks.mux.RUnlock()
	if len(o.asks.Book) == 0 || !o.snapShoted {
		return [][]string{}, false
	}
	book := o.asks.Book
	return book, true
}

func (o *OrderBookBranch) GetAsksEnoughForValue(value decimal.Decimal) ([][]string, bool) {
	o.asks.mux.RLock()
	defer o.asks.mux.RUnlock()
	if len(o.asks.Book) == 0 || !o.snapShoted {
		return [][]string{}, false
	}
	var loc int
	var sumValue decimal.Decimal
	for level, data := range o.asks.Book {
		if len(data) != 2 {
			return [][]string{}, false
		}
		price, _ := decimal.NewFromString(data[0])
		size, _ := decimal.NewFromString(data[1])
		sumValue = sumValue.Add(price.Mul(size))
		if sumValue.GreaterThan(value) {
			loc = level
			break
		}
	}
	book := o.asks.Book[:loc+1]
	return book, true
}

func (o *OrderBookBranch) GetAskMicro(idx int) (*bookMicro, bool) {
	o.asks.mux.RLock()
	defer o.asks.mux.RUnlock()
	if len(o.asks.Book) == 0 || !o.snapShoted {
		return nil, false
	}
	micro := o.asks.Micro[idx]
	return &micro, true
}

func (o *OrderBookBranch) GetBuyImpactNotion() decimal.Decimal {
	o.buyTrade.mux.RLock()
	defer o.buyTrade.mux.RUnlock()
	var total decimal.Decimal
	now := time.Now()
	for i, st := range o.buyTrade.Stamp {
		if now.After(st.Add(o.LookBack)) {
			continue
		}
		total = total.Add(o.buyTrade.Notional[i])
	}
	return total
}

func (o *OrderBookBranch) GetSellImpactNotion() decimal.Decimal {
	o.sellTrade.mux.RLock()
	defer o.sellTrade.mux.RUnlock()
	var total decimal.Decimal
	now := time.Now()
	for i, st := range o.sellTrade.Stamp {
		if now.After(st.Add(o.LookBack)) {
			continue
		}
		total = total.Add(o.sellTrade.Notional[i])
	}
	return total
}

func (o *OrderBookBranch) CalBidCumNotional() (decimal.Decimal, bool) {
	if len(o.bids.Book) == 0 {
		return decimal.NewFromFloat(0), false
	}
	if o.fromLevel > o.toLevel {
		return decimal.NewFromFloat(0), false
	}
	o.bids.mux.RLock()
	defer o.bids.mux.RUnlock()
	var total decimal.Decimal
	for level, item := range o.bids.Book {
		if level >= o.fromLevel && level <= o.toLevel {
			price, _ := decimal.NewFromString(item[0])
			qty, _ := decimal.NewFromString(item[1])
			total = total.Add(qty.Mul(price))
		} else if level > o.toLevel {
			break
		}
	}
	return total, true
}

func (o *OrderBookBranch) CalAskCumNotional() (decimal.Decimal, bool) {
	if len(o.asks.Book) == 0 {
		return decimal.NewFromFloat(0), false
	}
	if o.fromLevel > o.toLevel {
		return decimal.NewFromFloat(0), false
	}
	o.asks.mux.RLock()
	defer o.asks.mux.RUnlock()
	var total decimal.Decimal
	for level, item := range o.asks.Book {
		if level >= o.fromLevel && level <= o.toLevel {
			price, _ := decimal.NewFromString(item[0])
			qty, _ := decimal.NewFromString(item[1])
			total = total.Add(qty.Mul(price))
		} else if level > o.toLevel {
			break
		}
	}
	return total, true
}

func (o *OrderBookBranch) IsBigImpactOnBid() bool {
	impact := o.GetSellImpactNotion()
	rest, ok := o.CalBidCumNotional()
	if !ok {
		return false
	}
	micro, ok := o.GetBidMicro(o.fromLevel)
	if !ok {
		return false
	}
	if impact.GreaterThanOrEqual(rest) && micro.Trend == "cut" {
		return true
	}
	return false
}

func (o *OrderBookBranch) IsBigImpactOnAsk() bool {
	impact := o.GetBuyImpactNotion()
	rest, ok := o.CalAskCumNotional()
	if !ok {
		return false
	}
	micro, ok := o.GetAskMicro(o.fromLevel)
	if !ok {
		return false
	}
	if impact.GreaterThanOrEqual(rest) && micro.Trend == "cut" {
		return true
	}
	return false
}

func (o *OrderBookBranch) SetLookBackSec(input int) {
	o.LookBack = time.Duration(input) * time.Second
}

// top of the book is 1, to the level you want to sum all the notions
func (o *OrderBookBranch) SetImpactCumRange(toLevel int) {
	o.fromLevel = 0
	o.toLevel = toLevel - 1
}

func localOrderBook(product, symbol string, logger *log.Logger, streamTrade bool) *OrderBookBranch {
	var o OrderBookBranch
	o.SetLookBackSec(5)
	o.SetImpactCumRange(20)
	ctx, cancel := context.WithCancel(context.Background())
	o.cancel = &cancel
	bookticker := make(chan map[string]interface{}, 50)
	errCh := make(chan error, 1)
	symbol = strings.ToUpper(symbol)
	// stream orderbook
	orderBookErr := make(chan error, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				if err := binanceSocket(ctx, product, symbol, "@depth@100ms", logger, &bookticker, &orderBookErr); err == nil {
					return
				} else {
					if !strings.Contains(err.Error(), "reconnect because of time out") {
						errCh <- errors.New("Reconnect websocket")
					}
					logger.Warningf("Reconnect %s %s orderbook stream.\n", symbol, product)
					time.Sleep(time.Second)
				}
			}
		}
	}()
	// stream trade
	tradeErr := make(chan error, 1)
	if streamTrade {
		var tradeChannel string
		switch product {
		case "spot":
			tradeChannel = "@trade"
		case "swap":
			tradeChannel = "@aggTrade"
		}
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				default:
					if err := binanceSocket(ctx, product, symbol, tradeChannel, logger, &bookticker, &tradeErr); err == nil {
						return
					} else {
						if !strings.Contains(err.Error(), "reconnect because of time out") {
							errCh <- errors.New("Reconnect websocket")
						}
						logger.Warningf("Reconnect %s %s trade stream.\n", symbol, product)
						time.Sleep(time.Second)
					}
				}
			}
		}()
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
				o.maintainOrderBook(ctx, product, symbol, &bookticker, &errCh, &orderBookErr, &tradeErr)
				logger.Warningf("Refreshing %s %s local orderbook.\n", symbol, product)
				time.Sleep(time.Second)
			}
		}
	}()
	return &o
}

// default with look back 5 sec, impact range from 0 to 10 levels of the orderbook
func SpotLocalOrderBook(symbol string, logger *log.Logger, streamTrade bool) *OrderBookBranch {
	return localOrderBook("spot", symbol, logger, streamTrade)
}

// default with look back 5 sec, impact range from 0 to 10 levels of the orderbook
func SwapLocalOrderBook(symbol string, logger *log.Logger, streamTrade bool) *OrderBookBranch {
	return localOrderBook("swap", symbol, logger, streamTrade)
}

func (o *OrderBookBranch) maintainOrderBook(
	ctx context.Context,
	product, symbol string,
	bookticker *chan map[string]interface{},
	errCh *chan error,
	orderBookErr *chan error,
	tradeErr *chan error,
) {
	var storage []map[string]interface{}
	var linked bool = false
	o.snapShoted = false
	o.lastUpdatedId = decimal.NewFromInt(0)
	lastUpdate := time.Now()
	go func() {
		time.Sleep(time.Second)
		if err := o.GetOrderBookSnapShot(product, symbol); err != nil {
			*errCh <- err
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return
		case <-(*errCh):
			return
		case message := <-(*bookticker):
			event, ok := message["e"].(string)
			if !ok {
				continue
			}
			switch event {
			case "depthUpdate":
				if !o.snapShoted {
					storage = append(storage, message)
					continue
				}
				if len(storage) != 0 {
					for _, data := range storage {
						switch product {
						case "spot":
							if err := o.SpotUpdateJudge(&data, &linked); err != nil {
								*errCh <- err
							}
						case "swap":
							if err := o.SwapUpdateJudge(&data, &linked); err != nil {
								*errCh <- err
							}
						}
					}
					// clear storage
					storage = make([]map[string]interface{}, 0)
				}
				// handle incoming data
				switch product {
				case "spot":
					if err := o.SpotUpdateJudge(&message, &linked); err != nil {
						*errCh <- err
					}
				case "swap":
					if err := o.SwapUpdateJudge(&message, &linked); err != nil {
						*errCh <- err
					}
				}
				// update last update
				lastUpdate = time.Now()

			default:
				st := FormatingTimeStamp(message["T"].(float64))
				price, _ := decimal.NewFromString(message["p"].(string))
				size, _ := decimal.NewFromString(message["q"].(string))
				// is the buyer the mm
				var side string
				buyerIsMM := message["m"].(bool)
				if buyerIsMM {
					side = "sell"
				} else {
					side = "buy"
				}
				o.LocateTradeImpact(side, price, size, st)
				o.RenewTradeImpact()
			}
		default:
			if time.Now().After(lastUpdate.Add(time.Second * 10)) {
				// 10 sec without updating
				err := errors.New("reconnect because of time out")
				(*orderBookErr) <- err
				(*tradeErr) <- err
				return
			}
			time.Sleep(time.Millisecond * 100)
		}
	}
}

func (o *OrderBookBranch) LocateTradeImpact(side string, price, size decimal.Decimal, st time.Time) {
	switch side {
	case "buy":
		o.buyTrade.mux.Lock()
		defer o.buyTrade.mux.Unlock()
		o.buyTrade.Qty = append(o.buyTrade.Qty, size)
		o.buyTrade.Stamp = append(o.buyTrade.Stamp, st)
		o.buyTrade.Notional = append(o.buyTrade.Notional, price.Mul(size))
	case "sell":
		o.sellTrade.mux.Lock()
		defer o.sellTrade.mux.Unlock()
		o.sellTrade.Qty = append(o.sellTrade.Qty, size)
		o.sellTrade.Stamp = append(o.sellTrade.Stamp, st)
		o.sellTrade.Notional = append(o.sellTrade.Notional, price.Mul(size))
	}
}

func (o *OrderBookBranch) RenewTradeImpact() {
	var wg sync.WaitGroup
	wg.Add(2)
	now := time.Now()
	go func() {
		defer wg.Done()
		o.buyTrade.mux.Lock()
		defer o.buyTrade.mux.Unlock()
		var loc int = -1
		for i, st := range o.buyTrade.Stamp {
			if !now.After(st.Add(o.LookBack)) {
				break
			}
			loc = i
		}
		if loc == -1 {
			return
		}
		o.buyTrade.Stamp = o.buyTrade.Stamp[loc+1:]
		o.buyTrade.Qty = o.buyTrade.Qty[loc+1:]
		o.buyTrade.Notional = o.buyTrade.Notional[loc+1:]

	}()
	go func() {
		defer wg.Done()
		o.sellTrade.mux.Lock()
		defer o.sellTrade.mux.Unlock()
		var loc int = -1
		for i, st := range o.sellTrade.Stamp {
			if !now.After(st.Add(o.LookBack)) {
				break
			}
			loc = i
		}
		if loc == -1 {
			return
		}
		o.sellTrade.Stamp = o.sellTrade.Stamp[loc+1:]
		o.sellTrade.Qty = o.sellTrade.Qty[loc+1:]
		o.sellTrade.Notional = o.sellTrade.Notional[loc+1:]
	}()
	wg.Wait()
}

func (o *OrderBookBranch) SpotUpdateJudge(message *map[string]interface{}, linked *bool) error {
	headID := decimal.NewFromFloat((*message)["U"].(float64))
	tailID := decimal.NewFromFloat((*message)["u"].(float64))
	snapID := o.lastUpdatedId.Add(decimal.NewFromInt(1))
	if !(*linked) {
		//U <= lastUpdateId+1 AND u >= lastUpdateId+1.
		if headID.LessThanOrEqual(snapID) && tailID.GreaterThanOrEqual(snapID) {
			(*linked) = true
			o.UpdateNewComing(message)
			o.lastUpdatedId = tailID
		}
	} else {
		if headID.Equal(snapID) {
			o.UpdateNewComing(message)
			o.lastUpdatedId = tailID
		} else {
			return errors.New("refresh.")
		}
	}
	return nil
}

func (o *OrderBookBranch) SwapUpdateJudge(message *map[string]interface{}, linked *bool) error {
	headID := decimal.NewFromFloat((*message)["U"].(float64))
	tailID := decimal.NewFromFloat((*message)["u"].(float64))
	puID := decimal.NewFromFloat((*message)["pu"].(float64))
	snapID := o.lastUpdatedId
	if !(*linked) {
		// drop u is < lastUpdateId
		if tailID.LessThan(snapID) {
			return nil
		}
		// U <= lastUpdateId AND u >= lastUpdateId
		if headID.LessThanOrEqual(snapID) && tailID.GreaterThanOrEqual(snapID) {
			(*linked) = true
			o.UpdateNewComing(message)
			o.lastUpdatedId = tailID
		}
	} else {
		// new event's pu should be equal to the previous event's u
		if puID.Equal(snapID) {
			o.UpdateNewComing(message)
			o.lastUpdatedId = tailID
		} else {
			return errors.New("refresh.")
		}
	}
	return nil
}

func DecodingMap(message []byte, logger *log.Logger) (res map[string]interface{}, err error) {
	if message == nil {
		err = errors.New("the incoming message is nil")
		return nil, err
	}
	err = json.Unmarshal(message, &res)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func binanceSocket(ctx context.Context, product, symbol, channel string, logger *log.Logger, mainCh *chan map[string]interface{}, reCh *chan error) error {
	var w wS
	var duration time.Duration = 300
	w.Channel = channel
	w.Logger = logger
	w.OnErr = false
	var buffer bytes.Buffer
	switch product {
	case "spot":
		buffer.WriteString("wss://stream.binance.com:9443/ws/")
	case "swap":
		buffer.WriteString("wss://fstream3.binance.com/ws/")
	}
	buffer.WriteString(strings.ToLower(symbol))
	buffer.WriteString(w.Channel)
	url := buffer.String()
	conn, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return err
	}
	logger.Infof("Binance %s %s %s socket connected.\n", symbol, product, channel)
	w.Conn = conn
	defer conn.Close()
	if err := w.Conn.SetReadDeadline(time.Now().Add(time.Second * duration)); err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-(*reCh):
			return err
		default:
			if w.Conn == nil {
				d := w.OutBinanceErr()
				*mainCh <- d
				message := "Binance reconnect..."
				logger.Infoln(message)
				return errors.New(message)
			}
			_, buf, err := conn.ReadMessage()
			if err != nil {
				d := w.OutBinanceErr()
				*mainCh <- d
				message := "Binance reconnect..."
				logger.Infoln(message)
				return errors.New(message)
			}
			res, err1 := DecodingMap(buf, logger)
			if err1 != nil {
				d := w.OutBinanceErr()
				*mainCh <- d
				message := "Binance reconnect..."
				logger.Infoln(message)
				return errors.New(message)
			}
			err2 := w.HandleBinanceSocketData(res, mainCh)
			if err2 != nil {
				d := w.OutBinanceErr()
				*mainCh <- d
				message := "Binance reconnect..."
				logger.Infoln(message)
				return errors.New(message)
			}
			if err := w.Conn.SetReadDeadline(time.Now().Add(time.Second * duration)); err != nil {
				return err
			}
		}
	}
}

func (w *wS) HandleBinanceSocketData(res map[string]interface{}, mainCh *chan map[string]interface{}) error {
	event, ok := res["e"].(string)
	if !ok {
		return nil
	}
	switch event {
	case "depthUpdate":
		firstId := res["U"].(float64)
		lastId := res["u"].(float64)
		headID := decimal.NewFromFloat(firstId)
		tailID := decimal.NewFromFloat(lastId)
		if headID.LessThan(w.LastUpdatedId) {
			m := w.OutBinanceErr()
			*mainCh <- m
			return errors.New("got error when updating lastUpdateId")
		}
		w.LastUpdatedId = tailID
		*mainCh <- res
	case "trade":
		*mainCh <- res
	case "aggTrade":
		*mainCh <- res
	}
	return nil
}

func (w *wS) OutBinanceErr() map[string]interface{} {
	w.OnErr = true
	m := make(map[string]interface{})
	return m
}

func FormatingTimeStamp(timeFloat float64) time.Time {
	t := time.Unix(int64(timeFloat/1000), 0)
	return t
}
