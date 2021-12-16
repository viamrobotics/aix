package main

import (
	"bytes"
	"crypto/sha1"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"os"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"

	"github.com/AppImageCrafters/libzsync-go"
	"github.com/jessevdk/go-flags"
	"github.com/schollz/progressbar/v3"
)

func main() {
	// Set automatically inside AppImage runtimes
	appDir := os.Getenv("APPDIR")

	var opts struct {
		Update     bool   `long:"aix-update" description:"Update and exit"`
		AutoUpdate bool   `long:"aix-auto-update" description:"Update and run main app from new version"`
		UpdateURL  string `long:"aix-update-url" description:"Force ZSync (source) URL"`
		UpdateFile string `long:"aix-update-file" description:"Force local AppImage (destination) file path for update" env:"APPIMG"`
		Target     string `long:"aix-target" description:"Run internal tool/script (instead of main application)" env:"AIX_TARGET"`
		Install    bool   `long:"aix-install" description:"Shortcut for --aix-target=aix.d/install"`
		Help       bool   `long:"aix-help" description:"Show this help message"`
	}

	p := flags.NewParser(&opts, flags.IgnoreUnknown)
	p.Usage = "(AppImage eXtender) is a wrapper layer for use in AppImages.\n" +
		"  It allows self-updates and the running of non-default targets (such as install scripts) from within a single AppImage.\n" +
		"  Note that the options/arguments in this help message ONLY apply to the wrapper layer.\n" +
		"  All other options are passed through to the target application."

	args, err := p.Parse()
	if err != nil {
		panic(err)
	}

	if opts.Help {
		var b bytes.Buffer
		p.WriteHelp(&b)
		fmt.Println(b.String())
		os.Exit(1)
	}

	if opts.AutoUpdate {
		opts.Update = true
	}

	if opts.Install {
		if opts.Update {
			fmt.Println("Can't update and install at the same time. Please update first.")
			os.Exit(1)
		}
		_, err := os.Stat(appDir + "/aix.d/install")
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("No install target executable (aix.d/install) found!")
			os.Exit(1)
		}
		opts.Target = "aix.d/install"
	}

	if appDir != "" {
		opts.Target = appDir + "/" + strings.TrimPrefix(opts.Target, "/")
	}

	if opts.Update {
		if opts.UpdateFile == "" {
			panic("No AppImage file to update!")
		}

		if opts.UpdateURL == "" {
			var err error
			opts.UpdateURL, err = GetURLFromImage(opts.UpdateFile)
			if err != nil {
				panic(err)
			}
		}

		shaSum, err := GetSHA1(opts.UpdateFile)
		if err != nil {
			panic(err)
		}

		fmt.Println("Update: ", opts.Update)
		fmt.Println("AutoUpdate: ", opts.AutoUpdate)
		fmt.Println("URL: ", opts.UpdateURL)
		fmt.Println("File: ", opts.UpdateFile)
		fmt.Println("SHA1: ", shaSum)

		updated, err := doUpdate(opts.UpdateFile, opts.UpdateURL)
		if err != nil {
			fmt.Println("Error during update: ", err)
			os.Exit(1)
		}
		if updated {
			fmt.Println("Successfully updated.")
			if opts.AutoUpdate {
				// Clean environment
				os.Unsetenv("AIX_TARGET")

				// Exec the newly updated AppImage
				opts.Target = opts.UpdateFile
			}
		} else {
			fmt.Println("No update needed.")
		}
	}

	// Special env set within AppImage runtimes
	selfName := os.Getenv("ARGV0")
	if selfName == "" {
		// Fallback to normal ARGV0
		selfName = os.Args[0]
	}

	err = unix.Access(opts.Target, unix.X_OK)
	if err != nil {
		panic(fmt.Errorf("Target (%s) isn't executable", opts.Target))
	}

	env := os.Environ()

	newArgs := []string{selfName}
	newArgs = append(newArgs, args...)

	// We are completely replacing ourselves with the new app
	// This should never return, so we panic if it does
	panic(syscall.Exec(opts.Target, newArgs, env))
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

	bar := progressbar.DefaultBytes(zs.RemoteFileSize, "Updating")
	err = zs.Sync(filePath, &progressMultiWriter{bar, tmpFile})
	if err != nil {
		return false, err
	}
	bar.Finish()

	shaSum, err = GetSHA1(filePath)
	if err != nil {
		return false, err
	}

	if shaSum == zs.RemoteFileSHA1 {
		return false, fmt.Errorf("Checksum mismatch after update. Got: %s, Expected: %s", shaSum, zs.RemoteFileSHA1)
	}

	// So easy to get permissions
	fileInfo, _ := os.Stat(filePath)
	mode := fileInfo.Mode()

	// Then there's the two lines below... like demonic waterfowl, they slowly nibble away my sanity
	uid := int(fileInfo.Sys().(*syscall.Stat_t).Uid)
	gid := int(fileInfo.Sys().(*syscall.Stat_t).Gid)

	err = os.Rename(tmpFile.Name(), filePath)
	if err != nil {
		return false, err
	}

	os.Chown(filePath, uid, gid)
	if err != nil {
		return false, err
	}

	err = os.Chmod(filePath, mode)
	if err != nil {
		return false, err
	}

	return true, nil
}

// Simple io.WriteSeeker type for progress bar
type progressMultiWriter struct {
	progressBar io.Writer
	outFile     io.WriteSeeker
}

func (pmw *progressMultiWriter) Write(p []byte) (n int, err error) {
	for _, w := range []io.Writer{pmw.progressBar, pmw.outFile} {
		n, err = w.Write(p)
		if err != nil {
			return
		}
		if n != len(p) {
			err = io.ErrShortWrite
			return
		}
	}
	return len(p), nil
}

func (pmw *progressMultiWriter) Seek(offset int64, whence int) (int64, error) {
	return pmw.outFile.Seek(offset, whence)
}
