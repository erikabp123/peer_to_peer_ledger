package main

import (
	"bufio"
	"bytes"
	"encoding/gob"
	"fmt"
	"log"
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
	stop         = false
	mutexPeers   sync.Mutex
	mutexTracker sync.Mutex
	activePeers  []net.Conn
	mutexLedger  sync.Mutex
	tracker      *OrderedMap
	ledger       *Ledger
	port         string
	transactions map[string]bool
	myPublicKey  *account.PublicKey
	mySecretKey  *account.SecretKey
)

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
	myPublicKey, mySecretKey = account.KeyGen(256)
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
	for !stop {
		time.Sleep(5000 * time.Millisecond) // keep alive
	}
}

/* Exercise 6.13 */

type SignedTransaction struct {
	ID        string
	From      string
	To        string
	Amount    int
	Signature string
}

func (l *Ledger) SignedTransaction(t *SignedTransaction) {
	l.lock.Lock()
	defer l.lock.Unlock()
	fmt.Println("performing transaction #" + t.ID + "... " + t.From + " => " + t.To + "... Amount: " + strconv.Itoa(t.Amount))
	validSignature := true
	if validSignature {
		l.Accounts[t.From] -= t.Amount
		l.Accounts[t.To] += t.Amount
	}
}

/* End of exercise 6.13 */

type TcpMessage struct {
	Msg               string
	Peers             *OrderedMap
	SignedTransaction *SignedTransaction
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
	} else {
		fmt.Println("Connected to: " + ip)
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
	reader := bufio.NewReader(os.Stdin)
	for !stop {
		newMessage, _ := reader.ReadString('\n')
		newMessage = strings.TrimSuffix(newMessage, "\n")
		if strings.HasPrefix(newMessage, "t:") {
			sendToPeers(newMessage)
		}
		if newMessage == "getLedger" {
			fmt.Println(ledger.Accounts)
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
		marshal(*reply, conn)
		mutexTracker.Unlock()
		return
	}
	if strings.Contains(message.Msg, "Ready") {
		mutexTracker.Lock()
		ip := message.Peers.Keys[0]
		tracker.Set(ip, message.Peers.M[ip])
		mutexTracker.Unlock()
		return
	}
	if len(message.Peers.Keys) > 0 {
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
		go forwardTransaction(message)
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
		fmt.Println("New peer: " + newPeer.RemoteAddr().String())
		connect(newPeer)
	}
}

func sendToPeers(message string) {
	str := strings.Split(message, ":")
	str2 := strings.Split(str[1], ",")
	transaction := new(SignedTransaction)
	rand.Seed(time.Now().UTC().UnixNano())
	transaction.ID = strconv.Itoa(rand.Int())
	transaction.From = str2[0]
	transaction.To = str2[1]
	transaction.Amount, _ = strconv.Atoi(str2[2])
	transaction.Signature = ""
	tcpMsg := new(TcpMessage)
	tcpMsg.Msg = "Transaction"
	tcpMsg.SignedTransaction = transaction
	mutexLedger.Lock()
	if !transactions[transaction.ID] {
		transactions[transaction.ID] = true
		ledger.SignedTransaction(transaction)
	}
	mutexLedger.Unlock()
	mutexPeers.Lock()
	for _, peer := range activePeers {
		marshal(*tcpMsg, peer)
	}
	mutexPeers.Unlock()
}

func forwardTransaction(tcpMsg TcpMessage) {
	mutexLedger.Lock()
	if !transactions[tcpMsg.SignedTransaction.ID] {
		transactions[tcpMsg.SignedTransaction.ID] = true
		ledger.SignedTransaction(tcpMsg.SignedTransaction)
		mutexPeers.Lock()
		for _, peer := range activePeers {
			marshal(tcpMsg, peer)
		}
		mutexPeers.Unlock()
	}
	mutexLedger.Unlock()
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
