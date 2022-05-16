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
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Otterverse/libzsync-go"
	"github.com/jessevdk/go-flags"
	"github.com/schollz/progressbar/v3"
	//"github.com/alessio/shellescape"
)

func main() {

	// Set automatically inside AppImage runtimes
	appDir := os.Getenv("APPDIR")

	var opts struct {
		Update     bool   `long:"aix-update" description:"Update and exit"`
		// AutoUpdate bool   `long:"aix-auto-update" description:"Update and run main app from new version" env:"AIX_AUTO_UPDATE"`
		UseZSync   bool   `long:"aix-use-zsync" description:"Use zSync for update (slow, but bandwidth efficient)"`
		UpdateURL  string `long:"aix-update-url" description:"Force ZSync (source) URL" env:"AIX_UPDATE_URL"`
		UpdateFile string `long:"aix-update-file" description:"Force local AppImage (destination) file path for update" env:"APPIMAGE"`
		Target     string `long:"aix-target" description:"Run internal tool/script (instead of main application)" env:"AIX_TARGET"`
		Install    bool   `long:"aix-install" description:"Shortcut for --aix-target=aix.d/install"`
		PostUpdate bool   `long:"aix-post-update" description:"Run post-update tool/script at aix.d/postupdate (runs automatically after updates)" env:"AIX_POST_UPDATE"`
		Help       bool   `long:"aix-help" description:"Show this help message"`
	}

	p := flags.NewParser(&opts, flags.IgnoreUnknown)
	p.Usage = "(AppImage eXtender) is a wrapper layer for use in AppImages.\n" +
		"  It allows self-updates and the running of non-default targets (such as install scripts) from within a single AppImage.\n" +
		"  Note that the options/arguments in this help message ONLY apply to the wrapper layer.\n" +
		"  All other options are passed through to the target application."

	args, err := p.Parse()
	if err != nil {
		fmt.Println(err)
		return
	}

	var b bytes.Buffer
	p.WriteHelp(&b)
	helpString := b.String()

	if opts.Help {
		fmt.Println(helpString)
		return
	}

	// if opts.AutoUpdate {
	// 	opts.Update = true
	// }

	if appDir != "" {
		opts.Target = appDir + "/" + strings.TrimPrefix(opts.Target, "/")
	}

	if opts.PostUpdate {
		fmt.Println("Post update...")
		cmd := appDir + "/aix.d/postupdate"
		_, err := os.Stat(cmd)
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("No post-update needed")
			return
		} else if err != nil {
			fmt.Println(err)
			return
		}
		out, err := exec.Command(cmd).CombinedOutput()
		if err != nil {
			fmt.Printf("Post-update run failed: %s\n", out)
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Printf("Post-update run complete: %s\n", out)
		os.Unsetenv("AIX_POST_UPDATE")
		return
	}

	if opts.Install {
		if opts.Update {
			fmt.Println("Can't update and install at the same time. Please update first.")
			return
		}
		cmd := appDir + "/aix.d/install"
		_, err := os.Stat(cmd)
		if errors.Is(err, os.ErrNotExist) {
			fmt.Println("No install target executable (aix.d/install) found!")
			return
		} else if err != nil {
			fmt.Println(err)
			return
		}
		out, err := exec.Command(cmd).CombinedOutput()
		if err != nil {
			fmt.Printf("Install run failed: %s\n", out)
			fmt.Println(err)
			os.Exit(1)
		}
		fmt.Printf("Install run complete: %s\n", out)
		return
	}

	if opts.Update {
		if opts.UpdateFile == "" {
			fmt.Println("No AppImage file to update!")
			os.Exit(1)
		}

		if opts.UpdateURL == "" {
			var err error
			opts.UpdateURL, err = GetURLFromImage(opts.UpdateFile)
			if err != nil {
				fmt.Println(err)
				os.Exit(1)
			}
		}

		fmt.Println("UpdateURL: ", opts.UpdateURL)
		fmt.Println("UpdateFile: ", opts.UpdateFile)
		updated, err := doUpdate(opts.UpdateFile, opts.UpdateURL, opts.UseZSync)
		if err != nil {
			fmt.Println("Error during update: ", err)
			os.Exit(1)
		}

		if updated {
			fmt.Println("Successfully updated.")
			// Clean environment
			os.Unsetenv("AIX_TARGET")

			// Prep to run the post-update script
			os.Setenv("AIX_POST_UPDATE", "1")

			//cmd := shellescape.QuoteCommand(opts.UpdateFile)
			out, err := exec.Command("bash", "-c", opts.UpdateFile).CombinedOutput()
			fmt.Printf("Running post-update: %s\n", out)
			if err != nil {
				fmt.Println(err)
				//os.Exit(1)
			}

		} else if err == nil {
			fmt.Println("No update needed.")
		}
		return
	}

	if opts.Target == "" {
		fmt.Println("Error: no exectuable target set!")
		fmt.Println(helpString)
		return
	}

	err = unix.Access(opts.Target, unix.X_OK)
	if err != nil {
		fmt.Printf("Can't execute target '%s': %s", opts.Target, err)
		return
	}

	env := os.Environ()
	newArgs := []string{opts.Target}
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

func doUpdate(filePath string, url string, useZSync bool) (bool, error) {
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

	tmpFile, err := ioutil.TempFile(path.Dir(filePath), "aix-update-temp.")
	if err != nil {
		return false, err
	}
	defer os.Remove(tmpFile.Name())
	defer tmpFile.Close()

	// Systemd and other loggers don't handle the progress bar well
	shellPrompt := os.Getenv("TERM")
	var interactive bool
	if shellPrompt != "" {
		interactive = true
	}

	var bar *progressbar.ProgressBar
	var workers sync.WaitGroup
	if interactive {
		bar = progressbar.DefaultBytes(zs.RemoteFileSize, "Updating")
	} else {
		// If not in a shell, only print a few lines
		bar = progressbar.DefaultBytesSilent(zs.RemoteFileSize, "Updating")
		workers.Add(1)
		defer workers.Wait()
		go func() {
			defer workers.Done()
			for {
				state := bar.State()
				fmt.Printf(
					"Updating...  %.2f%% done | %d/%d bytes\n",
					state.CurrentPercent*100,
					int(state.CurrentBytes),
					int(zs.RemoteFileSize),
				)
				if state.CurrentPercent >= 1.0 {
					break
				}
				time.Sleep(time.Second)
			}
		}()
	}
	if useZSync {
		err = zs.Sync(filePath, &progressMultiWriter{bar, tmpFile})
	} else {
		err = downloadFile(zs.RemoteFileUrl, &progressMultiWriter{bar, tmpFile})
	}
	bar.Finish()
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

	// So easy to get permissions
	fileInfo, _ := os.Stat(filePath)
	mode := fileInfo.Mode()

	// Then there's the two lines below... like demonic waterfowl, they slowly nibble away my sanity
	uid := int(fileInfo.Sys().(*syscall.Stat_t).Uid)
	gid := int(fileInfo.Sys().(*syscall.Stat_t).Gid)

	fmt.Println(mode, uid, gid)

	// Real update starts, so don't let this interrupt in an ugly way
	signal.Ignore(syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	defer signal.Reset(syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// err = os.Rename(tmpFile.Name(), filePath)
	// if err != nil {
	// 	return false, err
	// }

	// os.Chown(filePath, uid, gid)
	// if err != nil {
	// 	return false, err
	// }

	// err = os.Chmod(filePath, mode)
	// if err != nil {
	// 	return false, err
	// }

	return true, nil
}

func downloadFile(url string, file io.Writer) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, err = io.Copy(file, resp.Body)
	return err
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
