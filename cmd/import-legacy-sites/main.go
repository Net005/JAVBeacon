package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/Net005/JAVBeacon/internal/legacyimport"
)

func main() {
	database := flag.String("database", "data/javbeacon.db", "target JAVBeacon SQLite database")
	sites := flag.String("sites", "data/REleaseMonitorSites.txt", "legacy ReleaseMonitorSites TSV file")
	reportPath := flag.String("report", "", "optional JSON report output path")
	apply := flag.Bool("apply", false, "replace matching sites and enable all sites; without this flag the command performs a dry run")
	flag.Parse()

	report, err := legacyimport.ReplaceSites(context.Background(), legacyimport.SiteOptions{DatabasePath: *database, SitesPath: *sites, Apply: *apply})
	output, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(output))
	if *reportPath != "" {
		if writeErr := os.WriteFile(*reportPath, append(output, '\n'), 0644); writeErr != nil {
			fmt.Fprintln(os.Stderr, "write report:", writeErr)
			os.Exit(1)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "legacy site import:", err)
		os.Exit(1)
	}
}
