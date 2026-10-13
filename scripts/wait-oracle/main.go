// Command wait-oracle waits until an Oracle database accepts logins (the listener
// opens well before the database): wait-oracle host:port service user password.
// CI uses it before the integration tests.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	goora "github.com/sijms/go-ora/v2"
)

func main() {
	if len(os.Args) != 5 {
		fmt.Fprintln(os.Stderr, "usage: wait-oracle host:port service user password")
		os.Exit(2)
	}
	host, port, err := net.SplitHostPort(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	n, _ := strconv.Atoi(port)
	db, err := sql.Open("oracle", goora.BuildUrl(host, n, os.Args[2], os.Args[3], os.Args[4], nil))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer func() { _ = db.Close() }()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = db.PingContext(ctx)
		cancel()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			fmt.Fprintln(os.Stderr, "Oracle did not start:", err)
			os.Exit(1)
		}
		time.Sleep(2 * time.Second)
	}
}
