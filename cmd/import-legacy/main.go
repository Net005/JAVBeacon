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
	results := flag.String("results", "data/ReleaseMonitorResults.txt", "legacy ReleaseMonitorResults TSV file")
	details := flag.String("details", "data/ReleaseMonitorVideosInfo.txt", "legacy ReleaseMonitorVideosInfo TSV file")
	reportPath := flag.String("report", "", "optional JSON report output path")
	provider := flag.String("provider", "", "optional source filter, for example JavLibrary")
	existingSitesOnly := flag.Bool("existing-sites-only", false, "skip results whose monitoring site is not already in the database")
	apply := flag.Bool("apply", false, "write the import; without this flag the command performs a dry run")
	flag.Parse()

	report, err := legacyimport.Run(context.Background(), legacyimport.Options{DatabasePath: *database, ResultsPath: *results, DetailsPath: *details, Provider: *provider, ExistingSitesOnly: *existingSitesOnly, Apply: *apply})
	output, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(output))
	if *reportPath != "" {
		if writeErr := os.WriteFile(*reportPath, append(output, '\n'), 0644); writeErr != nil {
			fmt.Fprintln(os.Stderr, "write report:", writeErr)
			os.Exit(1)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "legacy import:", err)
		os.Exit(1)
	}
}
