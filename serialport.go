package main

import (
	"bytes"
	"encoding/json"
	"github.com/johnlauer/goserial"
	"io"
	"log"
	"strconv"
)

type serport struct {
	// The serial port connection.
	portConf *serial.Config
	portIo   io.ReadWriteCloser

	// Keep track of whether we're being actively closed
	// just so we don't show scary error messages
	isClosing bool

	// counter incremented on queue, decremented on write
	itemsInBuffer int

	// buffered channel containing up to 25600 outbound messages.
	sendBuffered chan []byte

	// unbuffered channel of outbound messages that bypass internal serial port buffer
	sendNoBuf chan []byte

	// Do we have an extra channel/thread to watch our buffer?
	BufferType string
	//bufferwatcher *BufferflowDummypause
	bufferwatcher Bufferflow
}

type qwReport struct {
	Cmd  string
	QCnt int
	D    string
	Port string
}

type SpPortMessage struct {
	P string // the port, i.e. com22
	D string // the data, i.e. G0 X0 Y0
}

func (p *serport) reader() {
	//var buf bytes.Buffer
	for {
		ch := make([]byte, 1024)
		n, err := p.portIo.Read(ch)

		// read can return legitimate bytes as well as an error
		// so process the bytes if n > 0
		if n > 0 {
			//log.Print("Read " + strconv.Itoa(n) + " bytes ch: " + string(ch))
			data := string(ch[:n])
			//log.Print("The data i will convert to json is:")
			//log.Print(data)

			// give the data to our bufferflow so it can do it's work
			// to read/translate the data to see if it wants to block
			// writes to the serialport. each bufferflow type will decide
			// this on its own based on its logic, i.e. tinyg vs grbl vs others
			//p.b.bufferwatcher..OnIncomingData(data)
			p.bufferwatcher.OnIncomingData(data)

			//m := SpPortMessage{"Alice", "Hello"}
			m := SpPortMessage{p.portConf.Name, data}
			//log.Print("The m obj struct is:")
			//log.Print(m)

			//b, err := json.MarshalIndent(m, "", "\t")
			b, err := json.Marshal(m)
			if err != nil {
				log.Println(err)
				h.broadcastSys <- []byte("Error creating json on " + p.portConf.Name + " " +
					err.Error() + " The data we were trying to convert is: " + string(ch[:n]))
				break
			}
			//log.Print("Printing out json byte data...")
			//log.Print(string(b))
			h.broadcastSys <- b
			//h.broadcastSys <- []byte("{ \"p\" : \"" + p.portConf.Name + "\", \"d\": \"" + string(ch[:n]) + "\" }\n")
		}

		if p.isClosing {
			strmsg := "Shutting down reader on " + p.portConf.Name
			log.Println(strmsg)
			h.broadcastSys <- []byte(strmsg)
			break
		}

		if err == io.EOF || err == io.ErrUnexpectedEOF {
			// hit end of file
			log.Println("Hit end of file on serial port")
		}
		if err != nil {
			log.Println(err)
			h.broadcastSys <- []byte("Error reading on " + p.portConf.Name + " " +
				err.Error() + " Closing port.")
			break
		}

		// loop thru and look for a newline
		/*
			for i := 0; i < n; i++ {
				// see if we hit a newline
				if ch[i] == '\n' {
					// we are done with the line
					h.broadcastSys <- buf.Bytes()
					buf.Reset()
				} else {
					// append to buffer
					buf.WriteString(string(ch[:n]))
				}
			}*/
		/*
			buf.WriteString(string(ch[:n]))
			log.Print(string(ch[:n]))
			if string(ch[:n]) == "\n" {
				h.broadcastSys <- buf.Bytes()
				buf.Reset()
			}
		*/
	}
	p.portIo.Close()
}

// this method runs as its own thread because it's instantiated
// as a "go" method. so if it blocks inside, it is ok
func (p *serport) writerBuffered() {

	// this method can panic if user closes serial port and something is
	// in BlockUntilReady() and then a send occurs on p.sendNoBuf
	defer func() {
		if e := recover(); e != nil {
			// e is the interface{} typed-value we passed to panic()
			log.Println("Got panic: ", e) // Prints "Whoops: boom!"
		}
	}()

	// this for loop blocks on p.sendBuffered until that channel
	// sees something come in
	for data := range p.sendBuffered {

		log.Printf("Got p.sendBuffered. data:%v\n", string(data))

		// we want to block here if we are being asked
		// to pause.
		goodToGo := p.bufferwatcher.BlockUntilReady()

		if goodToGo == false {
			log.Println("We got back from BlockUntilReady() but apparently we must cancel this cmd")
			// since we won't get a buffer decrement in p.sendNoBuf, we must do it here
			p.itemsInBuffer--
		} else {
			// send to the non-buffered serial port writer
			log.Println("About to send to p.sendNoBuf channel")
			p.sendNoBuf <- data
		}
	}
	msgstr := "writerBuffered just got closed. make sure you make a new one. port:" + p.portConf.Name
	log.Println(msgstr)
	h.broadcastSys <- []byte(msgstr)
}

// this method runs as its own thread because it's instantiated
// as a "go" method. so if it blocks inside, it is ok
func (p *serport) writerNoBuf() {
	// this for loop blocks on p.send until that channel
	// sees something come in
	for data := range p.sendNoBuf {

		log.Printf("Got p.sendNoBuf. data:%v\n", string(data))

		// we want to block here if we are being asked
		// to pause. the problem is, how do we unblock
		//bufferBlockUntilReady(p.bufferwatcher)
		//p.bufferwatcher.BlockUntilReady()

		n2, err := p.portIo.Write(data)

		// if we get here, we were able to write successfully
		// to the serial port because it blocks until it can write

		// decrement counter
		p.itemsInBuffer--
		log.Printf("itemsInBuffer:%v\n", p.itemsInBuffer)
		//h.broadcastSys <- []byte("{\"Cmd\":\"Write\",\"QCnt\":" + strconv.Itoa(p.itemsInBuffer) + ",\"Byte\":" + strconv.Itoa(n2) + ",\"Port\":\"" + p.portConf.Name + "\"}")
		qwr := qwReport{
			Cmd:  "Write",
			QCnt: p.itemsInBuffer,
			D:    string(data),
			Port: p.portConf.Name,
		}
		json, _ := json.Marshal(qwr)
		h.broadcastSys <- json

		log.Print("Just wrote ", n2, " bytes to serial: ", string(data))
		//log.Print(n2)
		//log.Print(" bytes to serial: ")
		//log.Print(data)
		if err != nil {
			errstr := "Error writing to " + p.portConf.Name + " " + err.Error() + " Closing port."
			log.Print(errstr)
			h.broadcastSys <- []byte(errstr)
			break
		}
	}
	msgstr := "Shutting down writer on " + p.portConf.Name
	log.Println(msgstr)
	h.broadcastSys <- []byte(msgstr)
	p.portIo.Close()
}

func spHandlerOpen(portname string, baud int, buftype string) {

	log.Print("Inside spHandler")

	var out bytes.Buffer

	out.WriteString("Opening serial port ")
	out.WriteString(portname)
	out.WriteString(" at ")
	out.WriteString(strconv.Itoa(baud))
	out.WriteString(" baud")
	log.Print(out.String())

	//h.broadcast <- []byte("Opened a serial port bitches")
	h.broadcastSys <- out.Bytes()

	conf := &serial.Config{Name: portname, Baud: baud, RtsOn: true}
	log.Print("Created config for port")
	log.Print(conf)

	sp, err := serial.OpenPort(conf)
	log.Print("Just tried to open port")
	if err != nil {
		//log.Fatal(err)
		log.Print("Error opening port " + err.Error())
		//h.broadcastSys <- []byte("Error opening port. " + err.Error())
		h.broadcastSys <- []byte("{\"Cmd\":\"OpenFail\",\"Desc\":\"Error opening port. " + err.Error() + "\",\"Port\":\"" + conf.Name + "\",\"Baud\":" + strconv.Itoa(conf.Baud) + "}")

		return
	}
	log.Print("Opened port successfully")
	//p := &serport{send: make(chan []byte, 256), portConf: conf, portIo: sp}
	p := &serport{sendBuffered: make(chan []byte, 256*100), sendNoBuf: make(chan []byte), portConf: conf, portIo: sp, BufferType: buftype}

	// if user asked for a buffer watcher, i.e. tinyg/grbl then attach here
	if buftype == "tinyg" {

		bw := &BufferflowTinyg{Name: "tinyg"}
		bw.Init()
		bw.Port = portname
		p.bufferwatcher = bw

	} else if buftype == "dummypause" {

		// this is a dummy pause type bufferflow object
		// to test artificially a delay on the serial port write
		// it just pauses 3 seconds on each serial port write
		bw := &BufferflowDummypause{}
		bw.Init()
		bw.Port = portname
		p.bufferwatcher = bw

	} else {
		bw := &BufferflowDefault{}
		bw.Init()
		bw.Port = portname
		p.bufferwatcher = bw
	}

	sh.register <- p
	defer func() { sh.unregister <- p }()
	// this is internally buffered thread to not send to serial port if blocked
	go p.writerBuffered()
	// this is thread to send to serial port regardless of block
	go p.writerNoBuf()
	p.reader()
}

func spHandlerClose(p *serport) {
	p.isClosing = true
	// close the port
	p.portIo.Close()
	// unregister myself
	// we already have a deferred unregister in place from when
	// we opened. the only thing holding up that thread is the p.reader()
	// so if we close the reader we should get an exit
	h.broadcastSys <- []byte("Closing serial port " + p.portConf.Name)
}