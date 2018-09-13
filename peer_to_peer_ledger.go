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
	tracker      []string
	ledger       *Ledger
	port         string
	transactions map[string]bool
)

type TcpMessage struct {
	Msg         string
	Peers       []string
	Transaction *Transaction
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

type Transaction struct {
	ID     string
	From   string
	To     string
	Amount int
}

func (l *Ledger) Transaction(t *Transaction) {
	l.lock.Lock()
	fmt.Println("performing transaction #" + t.ID + "... " + t.From + " => " + t.To + "... Amount: " + strconv.Itoa(t.Amount))
	defer l.lock.Unlock()
	l.Accounts[t.From] -= t.Amount
	l.Accounts[t.To] += t.Amount
}

func main() {
	transactions = make(map[string]bool)
	port = randomPort()
	activePeers = []net.Conn{}
	tracker = []string{}
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
		//fmt.Println(tracker)
		//var a string
		//for _, value := range activePeers {
		//	a += value.RemoteAddr().String() + ", "
		//}
		//fmt.Println(a)
	}
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
		tracker = append(tracker, getMyIpAndPort())
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
		str := strings.Split(message.Msg, "_")
		tracker = append(tracker, str[1])
		mutexTracker.Unlock()
		return
	}
	if len(message.Peers) > 0 {
		mutexTracker.Lock()
		for _, newIp := range message.Peers {
			if !trackerContainsIp(newIp) {
				tracker = append(tracker, newIp)
			}
		}
		if !trackerContainsIp(getMyIpAndPort()) {
			tracker = append(tracker, getMyIpAndPort())
		}
		mutexTracker.Unlock()
		reply := new(TcpMessage)
		reply.Msg = "Ready_" + getMyIpAndPort()
		marshal(*reply, conn)
		return
	}
	if message.Msg == "Transaction" {
		go forwardTransaction(message)
	}

}

func trackerContainsIp(ip string) bool {
	for _, value := range tracker {
		if ip == value {
			return true
		}
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
	for key, value := range tracker {
		if value == getMyIpAndPort() {
			ourPosition = key
			break
		}
	}
	amountTilWrap = findWrapAround(len(tracker), ourPosition)
	for i := ourPosition + 1; i < len(tracker); i++ {
		if !activePeersContainsIp(tracker[i]) {
			go connectToExistingPeer(tracker[i])
		}
	}
	lessThan11 := len(tracker) < 11
	if lessThan11 {
		for i := 0; i < (len(tracker)-1)-amountTilWrap; i++ {
			if !activePeersContainsIp(tracker[i]) {
				go connectToExistingPeer(tracker[i])
			}
		}
	} else {
		for i := 0; i < 10-amountTilWrap; i++ {
			if !activePeersContainsIp(tracker[i]) {
				go connectToExistingPeer(tracker[i])
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
	transaction := new(Transaction)
	rand.Seed(time.Now().UTC().UnixNano())
	transaction.ID = strconv.Itoa(rand.Int())
	transaction.From = str2[0]
	transaction.To = str2[1]
	transaction.Amount, _ = strconv.Atoi(str2[2])
	tcpMsg := new(TcpMessage)
	tcpMsg.Msg = "Transaction"
	tcpMsg.Transaction = transaction
	mutexLedger.Lock()
	if !transactions[transaction.ID] {
		transactions[transaction.ID] = true
		ledger.Transaction(transaction)
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
	if !transactions[tcpMsg.Transaction.ID] {
		transactions[tcpMsg.Transaction.ID] = true
		ledger.Transaction(tcpMsg.Transaction)
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
