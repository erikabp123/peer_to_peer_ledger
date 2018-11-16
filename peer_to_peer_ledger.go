package main

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"encoding/json"
	"fmt"
	"log"
	"math/big"
	"math/rand"
	"net"
	"os"
	"peer_to_peer_ledger/account"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	stop                = false
	mutexPeers          sync.Mutex
	mutexTracker        sync.Mutex
	mutexAccountHolders sync.Mutex
	activePeers         []net.Conn
	mutexLedger         sync.Mutex
	tracker             *OrderedMap
	ledger              *Ledger
	port                string
	transactions        map[string]bool
	myPublicKey         *account.PublicKey
	mySecretKey         *account.SecretKey
	blocks              []Block
	accountHolders      [10]string
	lotteryStartTime    int64
	lastWinningSlot     int64
	wins                int64
	lotteryStarted      bool
)

type Block struct {
	PublicKey *account.PublicKey
	Slot      int
	Draw      big.Int
	Previous  big.Int
	U         *BlockData
}

type BlockData struct {
	Transactions []SignedTransaction
	Seed         *big.Int
	Hardness     *big.Int
}

type OrderedMap struct {
	M    map[string]*account.PublicKey
	Keys []string
}

func NewOrderedMap() *OrderedMap {
	om := new(OrderedMap)
	om.Keys = []string{}
	om.M = map[string]*account.PublicKey{}
	return om
}

func (o *OrderedMap) Set(k string, v *account.PublicKey) {
	o.M[k] = v
	o.Keys = append(o.Keys, k)
}

func main() {
	myPublicKey, mySecretKey = account.KeyGen(1024)
	transactions = make(map[string]bool)
	port = randomPort()
	activePeers = []net.Conn{}
	tracker = NewOrderedMap()
	ledger = MakeLedger()
	reader := bufio.NewReader(os.Stdin)
	accountHolders = [10]string{"", "", "", "", "", "", "", "", "", ""}
	ledger.Accounts["0"] = 10000000
	fmt.Printf("Connect to existing peer (E.g. 0.0.0.0:25556): ")
	ip, _ := reader.ReadString('\n')
	ip = strings.TrimSuffix(ip, "\n")
	connectToExistingPeer(ip)
	go accept()
	for !stop {
		time.Sleep(1000 * time.Millisecond) // keep alive
	}
}

/* Exercise 6.13 */

func NewSignedTransaction() *SignedTransaction {
	st := new(SignedTransaction)
	st.T = new(Transaction)
	return st
}

type SignedTransaction struct {
	T         *Transaction
	Signature string
}

type Transaction struct {
	ID     string
	From   string
	To     string
	Amount int
}

func (l *Ledger) SignedTransaction(t *SignedTransaction) {
	l.lock.Lock()
	defer l.lock.Unlock()
	if t.T.Amount <= 0 {
		return
	}
	if transactions[t.T.ID] {
		return
	}
	//fmt.Println("Transaction #" + t.T.ID + " " + t.T.From + " => " + t.T.To + " Amount: " + strconv.Itoa(t.T.Amount))

	//check signature
	n := new(big.Int)
	n, ok := n.SetString(t.Signature, 10)
	if !ok {
		fmt.Println("SetString: error")
		return
	}
	a, _ := strconv.Atoi(t.T.From)
	mutexAccountHolders.Lock()
	//fmt.Println("Compare:", account.Encrypt(*n, convertJSONStringToPublicKey(accountHolders[a])), "\nWith:", account.Hash(convertTransactionToBigInt(t.T)))
	validSignature := account.Verify(*n, convertTransactionToBigInt(t.T), convertJSONStringToPublicKey(accountHolders[a]))
	mutexAccountHolders.Unlock()
	//fmt.Println("Validating signature:", validSignature)
	if !validSignature {
		return
	}
	transactions[t.T.ID] = true
	l.Accounts[t.T.From] -= t.T.Amount
	l.Accounts[t.T.To] += t.T.Amount
	tcpMsg := new(TcpMessage)
	tcpMsg.Msg = "Transaction"
	tcpMsg.SignedTransaction = t
	tcpMsg.Peers = NewOrderedMap()
	forwardTransaction(tcpMsg)
}

/* End of exercise 6.13 */

type TcpMessage struct {
	Msg               string
	Peers             *OrderedMap
	SignedTransaction *SignedTransaction
	Blocks            []Block
	AccountHolders    [10]string
	StartTime         int64
}

type Ledger struct {
	Accounts map[string]int
	lock     sync.Mutex
}

func MakeLedger() *Ledger {
	ledger := new(Ledger)
	ledger.Accounts = make(map[string]int)
	return ledger
}

func marshal(msg TcpMessage, conn net.Conn) {
	var b bytes.Buffer
	e := gob.NewEncoder(&b)
	if err := e.Encode(msg); err != nil {
		panic(err)
	}
	conn.Write(b.Bytes())
}

func connectToExistingPeer(ip string) {
	fmt.Println("Connecting...")
	conn, err := net.Dial("tcp", ip)
	if err != nil {
		fmt.Println("Error connecting")
		tracker.Set(getMyIpAndPort(), myPublicKey)
		// Create genesis block
		t := make([]SignedTransaction, 10)
		h, s := account.KeyGen(512)
		mutexAccountHolders.Lock()
		accountHolders[0] = convertPublicKeyToJSON(h)
		mutexAccountHolders.Unlock()
		for i := 0; i < 10; i++ {
			n := strconv.Itoa(i + 1)
			ti := &SignedTransaction{
				T: &Transaction{
					ID:     n,
					From:   "0",
					To:     n,
					Amount: 1000000,
				},
				Signature: "0",
			}
			ti.Signature = account.Sign(account.Hash(convertTransactionToBigInt(ti.T)), s).String()
			t[i] = *ti
		}
		genesisBlock := new(Block)
		blockData := new(BlockData)
		blockData.Transactions = t
		hardness, seed := account.KeyGen(256)
		blockData.Seed = seed.D
		blockData.Hardness = hardness.N
		genesisBlock.U = blockData
		genesisBlock.PublicKey = h
		fmt.Println("Seed:", seed.D, "\nHardness:", hardness.N)
		processBlock(genesisBlock)
	} else {
		fmt.Println("Connected to: " + ip)
		connect(conn)
		tcpMessage := new(TcpMessage)
		tcpMessage.Msg = "Tracker"
		mutexAccountHolders.Lock()
		tcpMessage.AccountHolders = accountHolders
		mutexAccountHolders.Unlock()
		marshal(*tcpMessage, conn)
	}
}

func processBlock(Block *Block) {
	for _, t := range Block.U.Transactions {
		ledger.SignedTransaction(&t)
	}
	blocks = append(blocks, *Block)
}

func connect(conn net.Conn) {
	mutexPeers.Lock()
	activePeers = append(activePeers, conn)
	mutexPeers.Unlock()
	go listen(conn)
}

func userInput() {
	reader := bufio.NewReader(os.Stdin)
	check := false
	for !check {
		fmt.Print("Pick your account number 1-10: ")
		newMessage, _ := reader.ReadString('\n')
		newMessage = strings.TrimSuffix(newMessage, "\n")
		i, err := strconv.Atoi(newMessage)
		if err != nil {
			fmt.Println("Could not convert", newMessage)
		} else if i < 1 || i > 10 {
			fmt.Println("Must be between 1-10")
		} else if accountHolders[i] != "" {
			fmt.Println("This account is already claimed")
		} else {
			mutexAccountHolders.Lock()
			accountHolders[i] = convertPublicKeyToJSON(myPublicKey)
			send := new(TcpMessage)
			send.AccountHolders = accountHolders
			mutexAccountHolders.Unlock()
			send.Msg = "Claim"
			forwardTransaction(send)
			check = true
		}
	}
	fmt.Println("Available commands:\nsend *accountnumber* *value* (Creates and broadcasts a transaction)\nstart (Starts lottery for all peers in network)\nget ledger (Returns the current ledger)\nexit (Terminates)\n---------------------------------------")
	for !stop {
		newMessage, _ := reader.ReadString('\n')
		newMessage = strings.TrimSuffix(newMessage, "\n")
		if strings.HasPrefix(newMessage, "send ") {
			sendToPeers(newMessage)
		} else if newMessage == "get ledger" {
			for key, value := range ledger.Accounts {
				fmt.Println(key, value)
			}
		} else if newMessage == "exit" {
			fmt.Println("Exiting")
			stop = true
		} else if newMessage == "accounts" {
			mutexAccountHolders.Lock()
			fmt.Println(accountHolders)
			mutexAccountHolders.Unlock()
		} else if newMessage == "start" {
			msg := new(TcpMessage)
			time := time.Now().UnixNano()
			msg.Msg = "Start"
			msg.StartTime = time
			forwardTransaction(msg)
			go lottery(time)
		} else {
			fmt.Println("Could not understand", newMessage)
		}
	}
}

func listen(conn net.Conn) {
	for !stop {
		dec := gob.NewDecoder(conn)
		p := &TcpMessage{}
		dec.Decode(p)
		checkMessage(*p, conn)
	}
}

func checkMessage(message TcpMessage, conn net.Conn) {
	if message.Msg == "Tracker" {
		mutexTracker.Lock()
		reply := new(TcpMessage)
		reply.Peers = tracker
		reply.Msg = "Tracker List"
		mutexAccountHolders.Lock()
		reply.AccountHolders = accountHolders
		mutexAccountHolders.Unlock()
		reply.Blocks = blocks
		marshal(*reply, conn)
		mutexTracker.Unlock()
		return
	}
	if strings.Contains(message.Msg, "Ready") {
		mutexTracker.Lock()
		ip := message.Peers.Keys[0]
		tracker.Set(ip, message.Peers.M[ip])
		forward := new(TcpMessage)
		forward.Msg = "Forward"
		forward.Peers = tracker
		for _, conn := range activePeers {
			marshal(*forward, conn)
		}
		mutexTracker.Unlock()
		return
	}
	if message.Msg == "Tracker List" {
		mutexTracker.Lock()
		mutexAccountHolders.Lock()
		accountHolders = message.AccountHolders
		mutexAccountHolders.Unlock()
		for _, ip := range message.Peers.Keys {
			if !trackerContainsIp(ip) {
				tracker.Set(ip, message.Peers.M[ip])
			}
		}
		if !trackerContainsIp(getMyIpAndPort()) {
			tracker.Set(getMyIpAndPort(), myPublicKey)
		}
		for _, b := range message.Blocks {
			processBlock(&b)
		}
		mutexTracker.Unlock()
		reply := new(TcpMessage)
		reply.Msg = "Ready"
		myInfo := NewOrderedMap()
		myInfo.Set(getMyIpAndPort(), myPublicKey)
		reply.Peers = myInfo
		marshal(*reply, conn)
		return
	}
	if message.Msg == "Transaction" {
		go ledger.SignedTransaction(message.SignedTransaction)
	}
	if message.Msg == "Claim" {
		mutexAccountHolders.Lock()
		accountHolders = message.AccountHolders
		mutexAccountHolders.Unlock()
	}
	if message.Msg == "Forward" {
		if len(tracker.Keys) < len(message.Peers.Keys) {
			fmt.Println("Updating tracker list")
			mutexTracker.Lock()
			for _, p := range message.Peers.Keys {
				if !trackerContainsIp(p) {
					tracker.Set(p, message.Peers.M[p])
					connectToExistingPeer(p)
				}
			}
			mutexTracker.Unlock()
		}
		return
	}
	if message.Msg == "Start" && !lotteryStarted {
		go lottery(message.StartTime)
	}
}

func trackerContainsIp(ip string) bool {
	_, isInList := tracker.M[ip]
	if isInList {
		return true
	}
	return false
}

func activePeersContainsIp(ip string) bool {
	mutexPeers.Lock()
	defer mutexPeers.Unlock()
	for _, value := range activePeers {
		if ip == value.RemoteAddr().String() {
			return true
		}
	}
	return false
}

func connectToTrackerList() {
	mutexTracker.Lock()
	var amountTilWrap int
	var ourPosition int
	for key, value := range tracker.Keys {
		if value == getMyIpAndPort() {
			ourPosition = key
			break
		}
	}
	amountTilWrap = findWrapAround(len(tracker.Keys), ourPosition)
	for i := ourPosition + 1; i < len(tracker.Keys); i++ {
		ip := tracker.Keys[i]
		if !activePeersContainsIp(ip) {
			go connectToExistingPeer(ip)
		}
	}
	lessThan11 := len(tracker.Keys) < 11
	if lessThan11 {
		for i := 0; i < (len(tracker.Keys)-1)-amountTilWrap; i++ {
			ip := tracker.Keys[i]
			if !activePeersContainsIp(ip) {
				go connectToExistingPeer(ip)
			}
		}
	} else {
		for i := 0; i < 10-amountTilWrap; i++ {
			ip := tracker.Keys[i]
			if !activePeersContainsIp(ip) {
				go connectToExistingPeer(ip)
			}
		}
	}
	mutexTracker.Unlock()
}

func findWrapAround(length int, currentPos int) int {
	return (length - 1) - currentPos
}

func getMyIpAndPort() string {
	return GetOutboundIP().String() + ":" + port
}

func accept() {
	fmt.Println("Now listening on " + getMyIpAndPort())
	ln, err := net.Listen("tcp", ":"+port)
	//connectToTrackerList() Doesn't work anyway
	if err != nil {
		log.Fatal(err)
		fmt.Println("Error listening to port " + port)
	}
	go userInput()
	for !stop {
		newPeer, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
			fmt.Println("Error accepting connection from peer")
		}
		fmt.Println("New peer")
		connect(newPeer)
	}
}

func convertPublicKeyToJSON(key *account.PublicKey) string {
	output, err := json.Marshal(key)
	if err != nil {
		panic(err)
	}
	return string(output)
}

func convertJSONStringToPublicKey(key string) *account.PublicKey {
	pk := &account.PublicKey{}
	err := json.Unmarshal([]byte(key), pk)
	if err != nil {
		panic(err)
	}
	return pk
}

func convertTransactionToBigInt(t *Transaction) *big.Int {
	transAsString := fmt.Sprintf("%#v", t)
	var b bytes.Buffer
	e := gob.NewEncoder(&b)
	if err := e.Encode(transAsString); err != nil {
		panic(err)
	}
	transactionInt := new(big.Int)
	transactionInt.SetBytes(b.Bytes())
	return transactionInt
}

func createTransaction(to string, amount int) *SignedTransaction {
	signedTransaction := NewSignedTransaction()
	s := time.Now().UnixNano()
	rand.Seed(s)
	signedTransaction.T.ID = strconv.FormatInt(s-1541376441136647000+rand.Int63n(999-1)+1, 10)
	signedTransaction.T.From = convertPublicKeyToJSON(myPublicKey)
	signedTransaction.T.To = convertPublicKeyToJSON(tracker.M[to])
	signedTransaction.T.Amount = amount
	hash := account.Hash(convertTransactionToBigInt(signedTransaction.T))
	signedTransaction.Signature = account.Sign(hash, mySecretKey).String()
	return signedTransaction
}

func sendToPeers(message string) {
	str := strings.Split(message, " ")
	to := str[1]
	amount, _ := strconv.Atoi(str[2])
	signedTransaction := createTransaction(to, amount)
	tcpMsg := new(TcpMessage)
	tcpMsg.Msg = "Transaction"
	tcpMsg.SignedTransaction = signedTransaction
	ledger.SignedTransaction(signedTransaction)
}

func forwardTransaction(tcpMsg *TcpMessage) {
	mutexPeers.Lock()
	for _, peer := range activePeers {
		marshal(*tcpMsg, peer)
	}
	mutexPeers.Unlock()
}

func randomPort() string {
	rand.Seed(time.Now().UTC().UnixNano())      // Random seed based on time
	return strconv.Itoa(rand.Intn(8999) + 1000) // Returns a random number between 1000-9999
}

func GetOutboundIP() net.IP { // https://stackoverflow.com/questions/23558425/how-do-i-get-the-local-ip-address-in-go
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	localAddr := conn.LocalAddr().(*net.UDPAddr)

	return localAddr.IP
}

/*
	START LOTTERY SYSTEM
*/

func lottery(startTime int64) {
	lotteryStarted = true
	lotteryStartTime = startTime
	var previousSlot int64
	previousSlot = 0
	lastWinningSlot = 0
	for previousSlot < 240 {
		slot := calculateSlot()
		if slot > previousSlot {
			draw, result := drawAndCheck(slot)
			if result {
				adjustHardness(slot)
				fmt.Println("Verified winner:", verifyWinner(draw, slot, myPublicKey))
				wins++
				lastWinningSlot = slot
			} else if slot%10 == 0 {
				adjustHardness(slot)
			}
			previousSlot = slot
		}

	}
	fmt.Println(wins, previousSlot)
}

func verifyWinner(draw *big.Int, slot int64, pkOfOwner *account.PublicKey) bool {
	info := make([]*big.Int, 2)
	info[0] = convertSlotToBigInt(slot)
	info[1] = getSeed()
	verified := account.VerifyNoHash(draw, convertBigIntSliceToBigInt(info), pkOfOwner)
	return verified
}

func drawAndCheck(slot int64) (*big.Int, bool) {
	draw := draw(slot)
	val := valueOfDraw(draw, myPublicKey, slot)
	comparison := compareValueOfDrawWithHardness(val)
	if comparison < 1 {
		fmt.Println("Loss on draw!")
		return draw, false
	}
	fmt.Println("Win on draw!") // TODO: Implement actual win behvaior
	return draw, true
}

func calculateSlot() int64 {
	now := time.Now().UnixNano()
	slotNumber := (now - lotteryStartTime) / 1000000000
	return slotNumber
}

func draw(slot int64) *big.Int {
	info := make([]*big.Int, 2)
	info[0] = convertSlotToBigInt(slot)
	info[1] = getSeed()
	sig := account.Sign(convertBigIntSliceToBigInt(info), mySecretKey)
	return sig
}

func valueOfDraw(draw *big.Int, pkOfDraw *account.PublicKey, slot int64) *big.Int {
	tickets := new(big.Int)
	tickets.SetUint64(1) // TODO: Update to use the tickets associated with the pk of the draw
	toBeHashed := make([]*big.Int, 4)
	toBeHashed[0] = getSeed()
	toBeHashed[1] = convertSlotToBigInt(slot)
	toBeHashed[2] = convertPublicKeyToBigInt(pkOfDraw)
	toBeHashed[3] = draw
	h := account.Hash(convertBigIntSliceToBigInt(toBeHashed))
	val := tickets.Mul(tickets, h)
	return val
}

func compareValueOfDrawWithHardness(valueOfDraw *big.Int) int {
	return valueOfDraw.Cmp(getHardness())
}

func adjustHardness(slot int64) {
	idealTopRange := lastWinningSlot + 10
	hardness := getHardness()
	if slot > idealTopRange {
		//decrease hardness by 10%
		fmt.Println("Reducing hardness!")
		reduction := new(big.Int)
		reduction.Div(hardness, new(big.Int).SetUint64(10))
		hardness = hardness.Sub(hardness, reduction)
	} else if slot < lastWinningSlot+8 {
		//increase hardness by 20%
		fmt.Println("Increasing hardness")
		increase := new(big.Int)
		increase.Div(hardness, new(big.Int).SetUint64(10))
		hardness = hardness.Add(hardness, increase)
	}
}

func getHardness() *big.Int {
	return blocks[0].U.Hardness
}

func getSeed() *big.Int {
	return blocks[0].U.Seed
	//return blocks[0].U.Seed
}

func convertSlotToBigInt(slot int64) *big.Int {
	slotAsBigInt := new(big.Int)
	slotAsBigInt.SetInt64(slot)
	return slotAsBigInt
}

func convertBigIntSliceToBigInt(slice []*big.Int) *big.Int {
	str := fmt.Sprintf("%#v", slice)
	var b bytes.Buffer
	e := gob.NewEncoder(&b)
	if err := e.Encode(str); err != nil {
		panic(err)
	}
	sliceAsInt := new(big.Int)
	sliceAsInt.SetBytes(b.Bytes())
	return sliceAsInt
}

func convertPublicKeyToBigInt(key *account.PublicKey) *big.Int {
	var b bytes.Buffer
	e := gob.NewEncoder(&b)
	if err := e.Encode(key); err != nil {
		panic(err)
	}
	keyAsInt := new(big.Int)
	keyAsInt.SetBytes(b.Bytes())
	return keyAsInt
}

/*
	END LOTTERY SYSTEM
*/
