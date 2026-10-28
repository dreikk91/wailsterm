package main

import (
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/pkg/sftp"
	"github.com/wailsapp/wails/v3/pkg/application"
)

type FileEntry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Mode    string `json:"mode"`
	ModTime int64  `json:"modTime"`
	IsDir   bool   `json:"isDir"`
}

type Listing struct {
	Path    string      `json:"path"`
	Entries []FileEntry `json:"entries"`
}

// ponytail: new sftp subsystem per call; cache one per session if listing latency matters.
func (t *TermService) withSftp(id string, fn func(c *sftp.Client) error) error {
	s := t.get(id)
	if s == nil || s.client == nil {
		return errors.New("not an SSH session")
	}
	c, err := sftp.NewClient(s.client)
	if err != nil {
		return err
	}
	defer c.Close()
	return fn(c)
}

// SftpList lists dir; empty dir means the remote home directory.
func (t *TermService) SftpList(id, dir string) (Listing, error) {
	var l Listing
	err := t.withSftp(id, func(c *sftp.Client) error {
		if dir == "" {
			wd, err := c.Getwd()
			if err != nil {
				return err
			}
			dir = wd
		}
		infos, err := c.ReadDir(dir)
		if err != nil {
			return err
		}
		l.Path = path.Clean(dir)
		l.Entries = make([]FileEntry, 0, len(infos))
		for _, fi := range infos {
			isDir := fi.IsDir()
			if fi.Mode()&os.ModeSymlink != 0 {
				if st, err := c.Stat(path.Join(dir, fi.Name())); err == nil {
					isDir = st.IsDir()
				}
			}
			l.Entries = append(l.Entries, FileEntry{fi.Name(), fi.Size(), fi.Mode().String(), fi.ModTime().Unix(), isDir})
		}
		sort.Slice(l.Entries, func(i, j int) bool {
			a, b := l.Entries[i], l.Entries[j]
			if a.IsDir != b.IsDir {
				return a.IsDir
			}
			return a.Name < b.Name
		})
		return nil
	})
	return l, err
}

func (t *TermService) SftpMkdir(id, p string) error {
	return t.withSftp(id, func(c *sftp.Client) error { return c.MkdirAll(p) })
}

func (t *TermService) SftpRename(id, from, to string) error {
	return t.withSftp(id, func(c *sftp.Client) error { return c.Rename(from, to) })
}

func (t *TermService) SftpRemove(id, p string) error {
	return t.withSftp(id, func(c *sftp.Client) error { return c.RemoveAll(p) })
}

// SftpDownload asks for a local destination and copies the remote file there. Returns "" if cancelled.
func (t *TermService) SftpDownload(id, remote string) (string, error) {
	local, err := application.Get().Dialog.SaveFile().SetFilename(path.Base(remote)).PromptForSingleSelection()
	if err != nil || local == "" {
		return "", err
	}
	return local, t.withSftp(id, func(c *sftp.Client) error {
		src, err := c.Open(remote)
		if err != nil {
			return err
		}
		defer src.Close()
		dst, err := os.Create(local)
		if err != nil {
			return err
		}
		if _, err := io.Copy(dst, src); err != nil {
			dst.Close()
			return err
		}
		return dst.Close()
	})
}

// SftpUpload asks for local files and copies them into remoteDir. Returns the uploaded count.
// ponytail: synchronous, no progress events; add a transfer queue for large files.
func (t *TermService) SftpUpload(id, remoteDir string) (int, error) {
	files, err := application.Get().Dialog.OpenFile().CanChooseFiles(true).PromptForMultipleSelection()
	if err != nil || len(files) == 0 {
		return 0, err
	}
	n := 0
	err = t.withSftp(id, func(c *sftp.Client) error {
		for _, f := range files {
			src, err := os.Open(f)
			if err != nil {
				return err
			}
			dst, err := c.Create(path.Join(remoteDir, filepath.Base(f)))
			if err != nil {
				src.Close()
				return err
			}
			_, err = io.Copy(dst, src)
			src.Close()
			if cerr := dst.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
			n++
		}
		return nil
	})
	return n, err
}
