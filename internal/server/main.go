package server

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
)

// Main runs the language server as the command name with args, as
// cmd/kcls and kc -lsp do, and returns the process's exit code.
//
//	kcls [-stdio] [-listen addr] [-log file] [-version]
//
// By default it serves one client on stdin and stdout. With -listen it
// accepts clients on a TCP address (":9999", as kotlin-lsp listens) and
// serves each in turn until it is interrupted. Logs go to stderr, or to
// the -log file.
func Main(name string, args []string, version string, stdin io.Reader, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Bool("stdio", true, "serve one client on stdin and stdout (the default)")
	listen := flags.String("listen", "", "accept clients on this TCP address instead of stdio, as `addr` (\":9999\")")
	logFile := flags.String("log", "", "write logs to `file` instead of stderr")
	showVersion := flags.Bool("version", false, "print the version and exit")
	flags.Usage = func() {
		fmt.Fprintf(stderr, "usage: %s [-stdio] [-listen addr] [-log file] [-version]\n\n%s is the kc Kotlin language server.\n\n", name, name)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}
	if flags.NArg() > 0 {
		flags.Usage()
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "kcls %s\n", version)
		return 0
	}
	logOut := stderr
	if *logFile != "" {
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", name, err)
			return 1
		}
		defer f.Close()
		logOut = f
	}
	logger := log.New(logOut, "kcls: ", log.LstdFlags|log.Lmicroseconds)
	opts := Options{Version: version, Logf: logger.Printf}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *listen == "" {
		return Serve(ctx, stdin, stdout, opts)
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", name, err)
		return 1
	}
	logger.Printf("listening on %s", ln.Addr())
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		c, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return 0
			}
			logger.Printf("accept: %v", err)
			return 1
		}
		logger.Printf("client %s connected", c.RemoteAddr())
		Serve(ctx, c, c, opts)
		c.Close()
		logger.Printf("client %s disconnected", c.RemoteAddr())
	}
}
