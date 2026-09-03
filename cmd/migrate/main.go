// migrate applies forward-only control-plane schema migrations.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/stanimirivanov/perfeng-control-plane/internal/postgres"
)

func main() {
	if execute() != nil {
		os.Exit(1)
	}
}
func execute() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	fmt.Println("Connecting to PostgreSQL for control-plane migrations...")
	repository, err := postgres.Open(ctx, os.Getenv("PERFENG_DATABASE_URL"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "Migration connection failed. Check PERFENG_DATABASE_URL and database availability; credentials are not printed.")
		return err
	}
	defer repository.Close()
	fmt.Println("Checking migration versions and applying pending migrations...")
	if err = repository.Migrate(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "Migration failed; the transaction was rolled back or its commit outcome is uncertain. Check database availability, privileges and migration ledger before retrying.")
		return err
	}
	fmt.Println("Control-plane schema is up to date.")
	return nil
}
