//go:build cgo

// run

package main

/*
#include <unistd.h>
static int logosCgoFds[2];
static int logosCgoMakePipe(void) { return pipe(logosCgoFds); }
static long logosCgoBlockRead(void) {
	char b = 0;
	ssize_t n;
	do {
		n = read(logosCgoFds[0], &b, 1);
	} while (n < 0);
	if (n == 1 && b == 'x') {
		return 1;
	}
	return -1;
}
static int logosCgoWriteByte(void) {
	if (write(logosCgoFds[1], "x", 1) == 1) {
		return 0;
	}
	return -1;
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"time"

	"logos/rt"
)

var errBoom = errors.New("boom")

func main() {
	if C.logosCgoMakePipe() != 0 {
		fmt.Println("pipe: make failed")
		return
	}
	tmp, err := os.CreateTemp("", "groupcgoreg")
	if err != nil {
		fmt.Println("regular:", err)
		return
	}
	if _, err := tmp.WriteString("regular-ok"); err != nil {
		fmt.Println("regular:", err)
		return
	}
	name := tmp.Name()
	if err := tmp.Close(); err != nil {
		fmt.Println("regular:", err)
		return
	}
	defer os.Remove(name)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Println("listen:", err)
		return
	}
	defer ln.Close()
	cli, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		fmt.Println("dial:", err)
		return
	}
	defer cli.Close()
	srv, err := ln.Accept()
	if err != nil {
		fmt.Println("accept:", err)
		return
	}
	defer srv.Close()
	var tcpErr, cgoErr, regErr error
	var cgoTook time.Duration
	t0 := time.Now()
	g, _ := rt.NewGroup(context.Background(), 0)
	g.Go(func(_ context.Context) error {
		_, err := cli.Read(make([]byte, 1))
		tcpErr = err
		return nil
	})
	g.Go(func(_ context.Context) error {
		if C.logosCgoBlockRead() != 1 {
			cgoErr = errors.New("cgo read disturbed")
		}
		cgoTook = time.Since(t0)
		return nil
	})
	g.Go(func(_ context.Context) error {
		b, err := os.ReadFile(name)
		if err != nil {
			regErr = err
			return nil
		}
		if string(b) != "regular-ok" {
			regErr = errors.New("regular content changed")
		}
		return nil
	})
	g.Go(func(_ context.Context) error {
		time.Sleep(50 * time.Millisecond)
		return errBoom
	})
	g.Go(func(_ context.Context) error {
		time.Sleep(400 * time.Millisecond)
		if C.logosCgoWriteByte() != 0 {
			return errors.New("cgo write failed")
		}
		return nil
	})
	werr := g.Wait()
	if regErr != nil {
		fmt.Println("regular:", regErr)
	} else {
		fmt.Println("regular: ok")
	}
	if !os.IsTimeout(tcpErr) {
		fmt.Println("tcp:", tcpErr)
	} else {
		fmt.Println("tcp: timeout")
	}
	switch {
	case cgoErr != nil:
		fmt.Println("cgo:", cgoErr)
	case cgoTook < 250*time.Millisecond:
		fmt.Println("cgo: early")
	default:
		fmt.Println("cgo: ok")
	}
	fmt.Println("group:", werr)
}
