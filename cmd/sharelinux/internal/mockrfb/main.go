// Command mockrfb is a minimal standard RFB 3.8 server for checking viewers
// without a Mac: VNC password login, a test-pattern screen in raw encoding, and
// a log of the pointer, key and clipboard events it receives.
package main

import (
	"crypto/des"
	"crypto/rand"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"math/bits"
	"net"
	"os"
)

// width and height are the size of the screen; -size changes them.
var width, height = 640, 400

// damage is the side of the square sent in each update; 0 sends the whole screen.
var damage int

func main() {
	listen := flag.String("listen", "127.0.0.1:5990", "address to listen on")
	password := flag.String("password", os.Getenv("MOCKRFB_PASSWORD"), "VNC password (or MOCKRFB_PASSWORD)")
	size := flag.String("size", "640x400", "screen size WxH")
	flag.IntVar(&damage, "damage", 0, "send only a square of this many pixels per update, 0 for the whole screen")
	flag.Parse()
	if _, err := fmt.Sscanf(*size, "%dx%d", &width, &height); err != nil || width < 16 || height < 16 {
		log.Fatal("bad -size ", *size)
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	log.Println("listening on", listener.Addr())
	for {
		conn, err := listener.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go serve(conn, *password)
	}
}

func vncResponse(password string, challenge []byte) []byte {
	var key [8]byte
	copy(key[:], password)
	for i := range key {
		key[i] = bits.Reverse8(key[i])
	}
	block, _ := des.NewCipher(key[:])
	response := make([]byte, 16)
	block.Encrypt(response[:8], challenge[:8])
	block.Encrypt(response[8:], challenge[8:])
	return response
}

// pixel is the test pattern: color bars with a moving marker column.
func pixel(x, y, marker int) [4]byte {
	bars := [][3]byte{{255, 255, 255}, {255, 255, 0}, {0, 255, 255}, {0, 255, 0}, {255, 0, 255}, {255, 0, 0}, {0, 0, 255}, {30, 30, 30}}
	color := bars[x*len(bars)/width]
	if y > height-60 {
		shade := byte(x * 255 / width)
		color = [3]byte{shade, shade, shade}
	}
	if x >= marker && x < marker+8 {
		color = [3]byte{0, 0, 0}
	}
	return [4]byte{color[2], color[1], color[0], 0}
}

// frame renders the rectangle at x, y of the test pattern.
func frame(marker, x0, y0, w, h int) []byte {
	pixels := make([]byte, 0, w*h*4)
	for y := y0; y < y0+h; y++ {
		for x := x0; x < x0+w; x++ {
			p := pixel(x, y, marker)
			pixels = append(pixels, p[:]...)
		}
	}
	return pixels
}

func serve(conn net.Conn, password string) {
	defer conn.Close()
	write := func(data ...byte) { conn.Write(data) }
	conn.Write([]byte("RFB 003.008\n"))
	version := make([]byte, 12)
	if _, err := io.ReadFull(conn, version); err != nil {
		return
	}
	write(1, 2)
	choice := make([]byte, 1)
	if _, err := io.ReadFull(conn, choice); err != nil || choice[0] != 2 {
		return
	}
	challenge := make([]byte, 16)
	rand.Read(challenge)
	conn.Write(challenge)
	response := make([]byte, 16)
	if _, err := io.ReadFull(conn, response); err != nil {
		return
	}
	if string(response) != string(vncResponse(password, challenge)) {
		reason := "wrong password"
		out := binary.BigEndian.AppendUint32(nil, 1)
		out = binary.BigEndian.AppendUint32(out, uint32(len(reason)))
		conn.Write(append(out, reason...))
		log.Println("login refused")
		return
	}
	write(0, 0, 0, 0)
	log.Println("login ok")
	io.ReadFull(conn, make([]byte, 1)) // ClientInit
	name := "mockrfb"
	init := binary.BigEndian.AppendUint16(nil, uint16(width))
	init = binary.BigEndian.AppendUint16(init, uint16(height))
	init = append(init, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 16, 8, 0, 0, 0, 0)
	init = binary.BigEndian.AppendUint32(init, uint32(len(name)))
	conn.Write(append(init, name...))
	marker := 0
	sentClipboard := false
	for {
		kind := make([]byte, 1)
		if _, err := io.ReadFull(conn, kind); err != nil {
			log.Println("viewer left")
			return
		}
		switch kind[0] {
		case 0: // SetPixelFormat
			io.ReadFull(conn, make([]byte, 19))
		case 2: // SetEncodings
			head := make([]byte, 3)
			io.ReadFull(conn, head)
			io.ReadFull(conn, make([]byte, 4*int(binary.BigEndian.Uint16(head[1:]))))
		case 3: // FramebufferUpdateRequest
			body := make([]byte, 9)
			io.ReadFull(conn, body)
			marker = (marker + 4) % (width - 8)
			update := []byte{0, 0, 0, 1}
			x0, y0, w, h := 0, 0, width, height
			if damage > 0 {
				w, h = min(damage, width), min(damage, height)
				x0 = (marker * 7) % (width - w + 1)
				y0 = (marker * 3) % (height - h + 1)
			}
			update = binary.BigEndian.AppendUint16(update, uint16(x0))
			update = binary.BigEndian.AppendUint16(update, uint16(y0))
			update = binary.BigEndian.AppendUint16(update, uint16(w))
			update = binary.BigEndian.AppendUint16(update, uint16(h))
			update = binary.BigEndian.AppendUint32(update, 0)
			conn.Write(append(update, frame(marker, x0, y0, w, h)...))
			if !sentClipboard {
				sentClipboard = true
				text := "text copied on the server"
				cut := []byte{3, 0, 0, 0}
				cut = binary.BigEndian.AppendUint32(cut, uint32(len(text)))
				conn.Write(append(cut, text...))
			}
		case 4: // KeyEvent
			body := make([]byte, 7)
			io.ReadFull(conn, body)
			fmt.Printf("key down=%d keysym=%#x\n", body[0], binary.BigEndian.Uint32(body[3:]))
		case 5: // PointerEvent
			body := make([]byte, 5)
			io.ReadFull(conn, body)
			fmt.Printf("pointer buttons=%#x x=%d y=%d\n", body[0], binary.BigEndian.Uint16(body[1:]), binary.BigEndian.Uint16(body[3:]))
		case 6: // ClientCutText
			head := make([]byte, 7)
			io.ReadFull(conn, head)
			text := make([]byte, binary.BigEndian.Uint32(head[3:]))
			io.ReadFull(conn, text)
			fmt.Printf("clipboard from viewer: %q\n", text)
		default:
			log.Println("unknown message", kind[0])
			return
		}
	}
}
