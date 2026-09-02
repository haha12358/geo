package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/haha12358/geo/geoip"
	"github.com/haha12358/geo/geosite"

	"github.com/spf13/cobra"
)

func init() {
	commandCheck.PersistentFlags().StringVarP(&dbType, "type", "t", "", "specify database type")
	commandCheck.PersistentFlags().StringVarP(&dbPath, "file", "f", "", "specify database file path")
	mainCommand.AddCommand(commandCheck)
}

var commandCheck = &cobra.Command{
	Use:   "check",
	Short: "Check geo resources availability",
	RunE:  check,
}

var descriptionPlaceholder = map[string]string{"PLACEHOLDER": "geo"}

func check(cmd *cobra.Command, args []string) error {
	var (
		ipPaths   []string
		sitePaths []string
		err       error
	)
	if dbPath == "" {
		ipPaths, err = findIP()
		if err != nil {
			fmt.Println("⚠", err)
		}
		sitePaths, err = findSite()
		if err != nil {
			fmt.Println("⚠", err)
		}
	} else {
		ipPaths = []string{dbPath}
		sitePaths = []string{dbPath}
	}

	for _, filePath := range ipPaths {
		fmt.Println("🔎Checking", filePath)

		var db *geoip.Database
		db, err = geoip.FromFile(filePath)
		if err != nil {
			fmt.Println("❌Failed to load GeoIP database!")
			fmt.Println("Error:", err)
			os.Stdout.WriteString("\n")
			continue
		}
		mmdb := db.Reader()
		if len(mmdb.Metadata.Description) == 0 {
			// fix verify failed when description is empty
			mmdb.Metadata.Description = descriptionPlaceholder
		}

		err = mmdb.Verify()
		if err == nil ||
			(db.SourceType == geoip.TypeMetaV0 &&
				strings.Contains(err.Error(), "not see in the data section")) {
			fmt.Println("👌Successfully verified GeoIP database!")
		} else {
			fmt.Println("❌Failed to verify GeoIP database!")
			fmt.Println("Error:", err)
		}
		mmdb.Close()
		if mmdb.Metadata.Description["PLACEHOLDER"] == "geo" {
			mmdb.Metadata.Description = nil
		}

		fmt.Print("🔢Type: ")
		switch db.SourceType {
		case geoip.TypeMaxmind, geoip.TypeSing, geoip.TypeMetaV0:
			fmt.Println(db.SourceType, "MMDB database")
			fmt.Println("⏰MMDB build time:", time.Unix(int64(mmdb.Metadata.BuildEpoch), 0))
			fmt.Println("📃MMDB metadata:")
			mmdbJSON, _ := json.MarshalIndent(mmdb.Metadata, "", "    ")
			os.Stdout.Write(mmdbJSON)
			os.Stdout.WriteString("\n")
		case geoip.TypeV2Ray:
			fmt.Println("V2Ray GeoIP database")
			fmt.Println("📒Total nodes:", mmdb.Metadata.NodeCount)
		default:
			fmt.Println("unknown:", mmdb.Metadata.DatabaseType)
		}
		os.Stdout.WriteString("\n")
	}

	for _, filePath := range sitePaths {
		fmt.Println("🔎Checking", filePath)

		var db *geosite.Database
		db, err = geosite.FromFile(filePath)
		if err != nil {
			fmt.Println("❌Failed to load GeoSite database!")
			fmt.Println("Error:", err)
			os.Stdout.WriteString("\n")
			continue
		}

		fmt.Println("👌Successfully verified GeoSite database!")
		fmt.Print("🔢Type: ")
		switch db.SourceType {
		case geosite.TypeV2Ray:
			fmt.Println("V2Ray GeoSite database")
			fmt.Println("📒Total codes:", db.CodeCount)
		case geosite.TypeSing:
			fmt.Println("sing-geosite database")
			fmt.Println("📒Total codes:", db.CodeCount)
		default:
			fmt.Println("unknown:", db.SourceType)
		}
		os.Stdout.WriteString("\n")
	}

	return nil
}
