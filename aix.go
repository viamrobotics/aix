package main

import (
	"crypto/sha1"
	"debug/elf"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/AppImageCrafters/libzsync-go"
)

func main() {
	var update bool
	flag.BoolVar(&update, "aix-update", false, "Update and exit")

	var autoUpdate bool
	flag.BoolVar(&autoUpdate, "aix-auto-update", false, "Auto update and continue running")

	var url string
	flag.StringVar(&url, "aix-update-url", "", "Force ZSync (source) URL")

	var target string
	flag.StringVar(&target, "aix-update-file", "", "Force local (destination) file path")

	flag.Parse()
	args := flag.Args()
	if len(args) == 0 && target == "" {
		panic("Target File path expected")
	}

	if target == "" {
		target = args[0]
	}

	if url == "" {
		var err error
		url, err = GetURLFromImage(target)
		if err != nil {
			panic(err)
		}
	}

	shaSum, err := GetSHA1(target)
	if err != nil {
		panic(err)
	}

	fmt.Println("Update: ", update)
	fmt.Println("AutoUpdate: ", autoUpdate)
	fmt.Println("URL: ", url)
	fmt.Println("File: ", target)
	fmt.Println("SHA1: ", shaSum)

	if update {
		updated, err := doUpdate(target, url)
		if err != nil {
			fmt.Println("Error during update: ", err)
			os.Exit(1)
		}
		if updated {
			fmt.Println("Updated!")
		} else {
			fmt.Println("No update needed.")
		}
	}
}

func GetSHA1(filePath string) (string, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	h := sha1.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

func GetURLFromImage(filePath string) (string, error) {
	elfFile, err := elf.Open(filePath)
	if err != nil {
		return "", err
	}
	section := elfFile.Section(".upd_info")
	if section == nil {
		return "", fmt.Errorf("No .upd_info section in target file header")
	}
	sectionData, err := section.Data()
	if err != nil {
		return "", err
	}
	url := string(sectionData)
	if !strings.HasPrefix(url, "zsync|http") {
		return "", fmt.Errorf("Update URL not in zsync format")
	}
	url = strings.Split(url, "|")[1]

	return strings.Trim(url, "\x00"), nil
}

func doUpdate(filePath string, url string) (bool, error) {
	err := unix.Access(filePath, unix.W_OK|unix.R_OK)
	if err != nil {
		return false, fmt.Errorf("Need read/write access to update file. Try running with sudo.")
	}

	zs, err := zsync.NewZSync(url)
	if err != nil {
		return false, err
	}

	shaSum, err := GetSHA1(filePath)
	if err != nil {
		return false, err
	}

	if shaSum == zs.RemoteFileSHA1 {
		return false, nil
	}

	tmpFile, err := ioutil.TempFile("", "aix-update-temp.")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmpFile.Name())

	err = zs.Sync(filePath, tmpFile)
	if err != nil {
		return false, err
	}

	shaSum, err = GetSHA1(filePath)
	if err != nil {
		return false, err
	}

	if shaSum == zs.RemoteFileSHA1 {
		return false, fmt.Errorf("Checksum mismatch after update. Got: %s, Expected: %s", shaSum, zs.RemoteFileSHA1)
	}

	err = os.Rename(tmpFile.Name(), filePath)
	if err != nil {
		return false, err
	}

	return true, nil
}
