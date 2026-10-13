package backup

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/FelixBaek/arkism/internal/host"
)

// Encrypt a successful backup command, or authenticate/decrypt before publishing.
// Uses OpenSSL CMS AES-256-GCM. Private keys come as file paths, never as
// argument values. The output must not already exist. This does not provide
// snapshot consistency.

func init() { host.Register("backup-archive", archiveMain) }

// openssl is the binary that does all the cryptography.
var openssl = "openssl"

// archive runs mode "encrypt" (command → cert recipient) or "decrypt" (source with
// key) into a private temporary file, and links it to output only on success.
func archive(mode, output, cert string, command []string, source, key string) error {
	output, err := absolute(output)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(output); err == nil {
		return errors.New("output already exists")
	}
	// Mode 0600, same filesystem for the atomic link.
	target, err := os.CreateTemp(filepath.Dir(output), ".backup-")
	if err != nil {
		return err
	}
	temporary := target.Name()
	defer os.Remove(temporary)
	defer target.Close()
	if mode == "encrypt" {
		if len(command) == 0 {
			return errors.New("a backup command is required")
		}
		if err := encrypt(target, cert, command); err != nil {
			return err
		}
	} else {
		cmd := exec.Command(openssl, "cms", "-decrypt", "-binary", "-inform", "DER",
			"-in", source, "-recip", cert, "-inkey", key)
		cmd.Stdout = target
		if err := cmd.Run(); err != nil {
			var exit *exec.ExitError
			if errors.As(err, &exit) {
				return errors.New("decryption/authentication failed; output not published")
			}
			return err
		}
	}
	if err := target.Sync(); err != nil {
		return err
	}
	if err := target.Close(); err != nil {
		return err
	}
	// link is atomic and refuses a target created by another process.
	return os.Link(temporary, output)
}

func encrypt(target *os.File, cert string, command []string) error {
	read, write, err := os.Pipe()
	if err != nil {
		return err
	}
	producer := exec.Command(command[0], command[1:]...)
	producer.Stdout = write
	err = producer.Start()
	write.Close()
	if err != nil {
		read.Close()
		return err
	}
	cms := exec.Command(openssl, "cms", "-encrypt", "-binary", "-stream", "-aes-256-gcm",
		"-outform", "DER", "-recip", cert)
	cms.Stdin, cms.Stdout = read, target
	err = cms.Run()
	read.Close()
	if err != nil {
		producer.Process.Kill()
		producer.Wait()
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return errors.New("backup command or encryption failed; output not published")
		}
		return err
	}
	if err := producer.Wait(); err != nil {
		return errors.New("backup command or encryption failed; output not published")
	}
	return nil
}

func archiveMain(args []string, _ io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 || (args[0] != "encrypt" && args[0] != "decrypt") {
		fmt.Fprintln(stderr, "usage: arkism backup-archive {encrypt,decrypt} ...")
		return 2
	}
	mode := args[0]
	fs := flags("backup-archive "+mode, stderr)
	cert := fs.String("cert", "", "recipient certificate")
	output := fs.String("output", "", "new output file")
	input, key := new(string), new(string)
	required := []string{"cert", "output"}
	if mode == "decrypt" {
		fs.StringVar(input, "input", "", "encrypted bundle")
		fs.StringVar(key, "key", "", "private key file")
		required = append(required, "input", "key")
	}
	if code := parse(fs, args[1:], required...); code >= 0 {
		return code
	}
	var err error
	if mode == "encrypt" {
		// flag stops at "--" or the first non-option, so the rest is the backup command.
		err = archive(mode, *output, *cert, fs.Args(), "", "")
	} else if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "arkism backup-archive decrypt: error: unrecognized arguments: %v\n", fs.Args())
		return 2
	} else {
		err = archive(mode, *output, *cert, nil, *input, *key)
	}
	if err != nil {
		fmt.Fprintln(stderr, "backup archive failed; no new completed output was published")
		return 1
	}
	fmt.Fprintln(stdout, "backup archive completed")
	return 0
}
