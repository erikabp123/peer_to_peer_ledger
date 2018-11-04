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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	stop                    = false
	mutexPeers              sync.Mutex
	mutexTracker            sync.Mutex
	mutexTransactions       sync.Mutex
	activePeers             []net.Conn
	mutexUnsequenced        sync.Mutex
	tracker                 *OrderedMap
	ledger                  *Ledger
	port                    string
	transactions            map[string]bool
	unsequencedTransactions []*SignedTransaction
	myPublicKey             *account.PublicKey
	mySecretKey             *account.SecretKey
	lastBlock               = -1
	phase                   int
	sequencer               bool
)

type Block struct {
	BlockNumber int
	IDS         []string
}

type SignedBlock struct {
	B         *Block
	Signature *big.Int
}

type OrderedMap struct {
	M    map[string]*account.PublicKey
	Keys []string
}

func createBlock() *Block {
	block := new(Block)
	block.BlockNumber = lastBlock + 1
	for i := 0; i < len(unsequencedTransactions); i++ {
		block.IDS = append(block.IDS, unsequencedTransactions[i].T.ID)
	}
	unsequencedTransactions = []*SignedTransaction{}
	sort.Strings(block.IDS)
	lastBlock++
	return block
}

func signBlock(block *Block) *SignedBlock {
	blockAsInt := convertBlockToInt(block)
	signature := account.Sign(account.Hash(blockAsInt), mySecretKey)
	signedBlock := new(SignedBlock)
	signedBlock.B = block
	signedBlock.Signature = signature
	return signedBlock
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
	phase = 1
	initialize()
}

func initialize() {
	myPublicKey, mySecretKey = account.KeyGen(512)
	transactions = make(map[string]bool)
	port = randomPort()
	activePeers = []net.Conn{}
	tracker = NewOrderedMap()
	ledger = MakeLedger()
	reader := bufio.NewReader(os.Stdin)
	fmt.Printf("Connect to existing peer (E.g. 0.0.0.0:25556): ")
	ip, _ := reader.ReadString('\n')
	ip = strings.TrimSuffix(ip, "\n")
	connectToExistingPeer(ip)
	go userInput()
	go accept()
	go createBlocks()
	for !stop {
		time.Sleep(5000 * time.Millisecond) // keep alive
	}
}

func createBlocks() {
	if !sequencer {
		return
	}
	for !stop {
		signedBlock := signBlock(createBlock())
		reply := new(TcpMessage)
		reply.Msg = "Signed Block"
		reply.SignedBlock = signedBlock
		mutexPeers.Lock()
		for _, peer := range activePeers {
			marshal(*reply, peer)
		}
		mutexPeers.Unlock()
		fmt.Println("Sending block")
		time.Sleep(10000 * time.Millisecond)
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
	Authorizer string
	ID         string
	From       string
	To         string
	Amount     int
}

func (l *Ledger) SignedTransaction(t *SignedTransaction) {
	l.lock.Lock()
	defer l.lock.Unlock()
	mutexUnsequenced.Lock()
	mutexTransactions.Lock()
	if t.T.Amount <= 0 || transactions[t.T.ID] || find(t.T.ID) != -1 {
		fmt.Println("Did not create", t.T.ID)
		mutexUnsequenced.Unlock()
		mutexTransactions.Unlock()
		return
	}
	mutexTransactions.Unlock()
	//check signature
	n := new(big.Int)
	n, ok := n.SetString(t.Signature, 10)
	if !ok {
		fmt.Println("SetString: error")
		mutexUnsequenced.Unlock()
		return
	}
	validSignature := account.Verify(n, convertTransactionToBigInt(t.T), tracker.M[t.T.Authorizer])
	fmt.Println("Validating signature:", validSignature)
	if !validSignature {
		mutexUnsequenced.Unlock()
		return
	}
	unsequencedTransactions = append(unsequencedTransactions, t)
	mutexUnsequenced.Unlock()
	tcpMsg := new(TcpMessage)
	tcpMsg.Msg = "Transaction"
	tcpMsg.SignedTransaction = t
	tcpMsg.Peers = NewOrderedMap()
	go forwardTransaction(tcpMsg)
}

/* End of exercise 6.13 */

type TcpMessage struct {
	Msg               string
	Peers             *OrderedMap
	SignedTransaction *SignedTransaction
	SignedBlock       *SignedBlock
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
		fmt.Println("You are now the sequencer. Awaiting phase two.")
		sequencer = true
		tracker.Set(getMyIpAndPort(), myPublicKey)
	} else {
		fmt.Println("Connected to: " + ip)
		sequencer = false
		connect(conn)
		tcpMessage := new(TcpMessage)
		tcpMessage.Msg = "Tracker"
		marshal(*tcpMessage, conn)
	}
}

func connect(conn net.Conn) {
	mutexPeers.Lock()
	activePeers = append(activePeers, conn)
	mutexPeers.Unlock()
	go listen(conn)
}

func userInput() {
	for {
		reader := bufio.NewReader(os.Stdin)
		newMessage, _ := reader.ReadString('\n')
		newMessage = strings.TrimSuffix(newMessage, "\n")
		if strings.HasPrefix(newMessage, "send ") {
			sendToPeers(newMessage)
		}
		if newMessage == "test" {
			fmt.Println("Performing test!")
			giveMoney(getMyIpAndPort(), 500)
			cmnd := new(TcpMessage)
			cmnd.Msg = "Test " + getMyIpAndPort()
			for _, conn := range activePeers {
				marshal(*cmnd, conn)
				time.Sleep(2 * time.Millisecond)
			}
		}
		if newMessage == "get ledger" {
			for key, value := range ledger.Accounts {
				fmt.Println(key, value)
			}
		}
	}
}

func listen(conn net.Conn) {
	for {
		dec := gob.NewDecoder(conn)
		p := &TcpMessage{}
		dec.Decode(p)
		go checkMessage(*p, conn)
	}
}

func checkMessage(message TcpMessage, conn net.Conn) {
	if phase == 1 && message.Msg == "Tracker" {
		mutexTracker.Lock()
		reply := new(TcpMessage)
		reply.Peers = tracker
		reply.Msg = "Tracker List"
		marshal(*reply, conn)
		mutexTracker.Unlock()
		return
	}
	if phase == 1 && message.Msg == "Forward" {
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
	if phase == 1 && message.Msg == "Ready" {
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
	if phase == 1 && message.Msg == "Tracker List" {
		mutexTracker.Lock()
		for _, ip := range message.Peers.Keys {
			if !trackerContainsIp(ip) {
				tracker.Set(ip, message.Peers.M[ip])
			}
		}
		if !trackerContainsIp(getMyIpAndPort()) {
			tracker.Set(getMyIpAndPort(), myPublicKey)
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
		if phase == 1 {
			fmt.Println("Phase 2")
			phase = 2
		}
		go ledger.SignedTransaction(message.SignedTransaction)
		return
	}
	if message.Msg == "Signed Block" {
		go processBlock(message.SignedBlock)
		return
	}
	if strings.Contains(message.Msg, "Test") {
		fmt.Println("Performing test!")
		str := strings.Split(message.Msg, " ")
		ip := str[1]
		giveMoney(ip, 500)
		for i := 0; i < 500; i++ {
			transaction := createTransaction(ip, getMyIpAndPort(), 1)
			ledger.SignedTransaction(transaction)
			time.Sleep(20 * time.Millisecond)
		}
		return
	}
	return
}

func convertBlockToInt(block *Block) *big.Int {
	str := fmt.Sprintf("%#v", block)
	var buffer bytes.Buffer
	enc := gob.NewEncoder(&buffer)
	err := enc.Encode(str)
	if err != nil {
		log.Fatal("encode error:", err)
	}
	blockAsInt := new(big.Int).SetBytes(buffer.Bytes())
	return blockAsInt
}

func processBlock(signedBlock *SignedBlock) {
	fmt.Println("Recieved block")
	ledger.lock.Lock()
	defer ledger.lock.Unlock()
	blockAsInt := convertBlockToInt(signedBlock.B)
	sequencerPK := tracker.M[tracker.Keys[0]]
	verification := account.Verify(signedBlock.Signature, blockAsInt, sequencerPK)
	if !verification {
		fmt.Println("Block failed verification!")
		return
	}
	block := signedBlock.B
	if lastBlock != -1 && block.BlockNumber != lastBlock+1 {
		fmt.Println("Wrong block number! Was:", block.BlockNumber, "... Expected:", lastBlock+1)
		return
	}
	lastBlock = block.BlockNumber

	for _, id := range block.IDS {
		mutexUnsequenced.Lock()
		i := find(id)
		if i != -1 {
			go performTransaction(unsequencedTransactions[i])
			unsequencedTransactions = append(unsequencedTransactions[:i], unsequencedTransactions[i+1:]...)
		} else {
			fmt.Println("Could not find transaction #" + id)
		}
		mutexUnsequenced.Unlock()
	}
}

func find(x string) int {
	for i, n := range unsequencedTransactions {
		if x == n.T.ID {
			return i
		}
	}
	return -1
}

func giveMoney(account string, amount int) {
	ledger.Accounts[account] += amount
}

func performTransaction(t *SignedTransaction) {
	mutexTransactions.Lock()
	fmt.Println("Transaction #" + t.T.ID + " " + t.T.From + " => " + t.T.To + " amount: " + strconv.Itoa(t.T.Amount))
	if ledger.Accounts[t.T.From] < t.T.Amount {
		mutexTransactions.Unlock()
		return
	}
	ledger.Accounts[t.T.From] -= t.T.Amount
	ledger.Accounts[t.T.To] += t.T.Amount
	transactions[t.T.ID] = true
	mutexTransactions.Unlock()
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
			connectToExistingPeer(ip)
		}
	}
	lessThan11 := len(tracker.Keys) < 11
	if lessThan11 {
		for i := 0; i < (len(tracker.Keys)-1)-amountTilWrap; i++ {
			ip := tracker.Keys[i]
			if !activePeersContainsIp(ip) {
				connectToExistingPeer(ip)
			}
		}
	} else {
		for i := 0; i < 10-amountTilWrap; i++ {
			ip := tracker.Keys[i]
			if !activePeersContainsIp(ip) {
				connectToExistingPeer(ip)
			}
		}
	}
	fmt.Println(activePeers)
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
	connectToTrackerList()
	if err != nil {
		log.Fatal(err)
		fmt.Println("Error listening to port " + port)
	}
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

func convertTransactionToBigInt(transaction *Transaction) *big.Int {
	var b bytes.Buffer
	e := gob.NewEncoder(&b)
	if err := e.Encode(transaction); err != nil {
		panic(err)
	}
	transactionInt := new(big.Int)
	transactionInt.SetBytes(b.Bytes())
	return transactionInt
}

func createTransaction(fromIP string, toIP string, amount int) *SignedTransaction {
	signedTransaction := NewSignedTransaction()
	rand.Seed(time.Now().UTC().UnixNano())
	signedTransaction.T.ID = strconv.Itoa(rand.Int())
	//signedTransaction.T.From = convertPublicKeyToJSON(tracker.M[fromIP])
	//signedTransaction.T.To = convertPublicKeyToJSON(tracker.M[toIP])
	signedTransaction.T.From = fromIP
	signedTransaction.T.To = toIP
	signedTransaction.T.Amount = amount
	signedTransaction.T.Authorizer = getMyIpAndPort()
	hash := account.Hash(convertTransactionToBigInt(signedTransaction.T))
	signedTransaction.Signature = account.Sign(hash, mySecretKey).String()
	return signedTransaction
}

func sendToPeers(message string) {
	str := strings.Split(message, " ")
	from := str[1]
	to := str[2]
	amount, _ := strconv.Atoi(str[3])
	signedTransaction := createTransaction(from, to, amount)
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
