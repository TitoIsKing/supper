package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/apex/log"
	"github.com/spf13/viper"
	"github.com/tympanix/supper/provider"
	"github.com/tympanix/supper/types"
)

type mediaExistsError struct{}

func (s *mediaExistsError) Error() string {
	return "media allready exists"
}

type renamer func(types.Local, string) error

// Rename is a wrapper function around a renamer which performs some sanity checks
func (r renamer) Rename(local types.Local, dest string, force bool) error {
	_, err := os.Stat(dest)
	if !force && err == nil {
		return &mediaExistsError{}
	}
	if err == nil {
		if err := os.Remove(dest); err != nil {
			return err
		}
		log.WithField("path", dest).Debug("Removed existing media")
	}
	return r(local, dest)
}

func copyRenamer(local types.Local, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), os.ModeDir); err != nil {
		return err
	}
	file, err := local.Open()
	if err != nil {
		return err
	}
	defer file.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, file)
	if err != nil {
		return err
	}
	log.WithField("path", dest).Debug("Media copied")
	return nil
}

func moveRenamer(local types.Local, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), os.ModeDir); err != nil {
		return err
	}
	mpath, ok := local.(types.Pather)
	if !ok {
		return errors.New("can't move media without a path")
	}
	if err := os.Rename(mpath.Path(), dest); err != nil {
		return err
	}
	log.WithField("path", dest).Debug("Media moved")
	return nil
}

func symlinkRenamer(local types.Local, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), os.ModeDir); err != nil {
		return err
	}
	mpath, ok := local.(types.Pather)
	if !ok {
		return errors.New("can't symlink media without a path")
	}
	if err := os.Symlink(mpath.Path(), dest); err != nil {
		return err
	}
	log.WithField("path", dest).Debug("Media symlinked")
	return nil
}

func hardlinkRenamer(local types.Local, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), os.ModeDir); err != nil {
		return err
	}
	mpath, ok := local.(types.Pather)
	if !ok {
		return errors.New("can't hardlink media without a path")
	}
	if err := os.Link(mpath.Path(), dest); err != nil {
		return err
	}
	log.WithField("path", dest).Debug("Media hardlinked")
	return nil
}

// Renamers holds the available renaming actions of the application
var Renamers = map[string]renamer{
	"copy":     renamer(copyRenamer),
	"move":     renamer(moveRenamer),
	"symlink":  renamer(symlinkRenamer),
	"hardlink": renamer(hardlinkRenamer),
}

var pathRegex = regexp.MustCompile(`[%/\?\\\*:\|"<>\n\r]`)

// cleanString cleans the string for unwanted characters such that it can
// be used safely as a name in a file hierarchy. All path seperators are
// removed from the string.
func cleanString(str string) string {
	return pathRegex.ReplaceAllString(str, "")
}

var spaceRegex = regexp.MustCompile(`\s\s+`)

// truncateSpaces replaces all consecutive space characters with a single space
func truncateSpaces(str string) string {
	return spaceRegex.ReplaceAllString(str, " ")
}

// RenameMedia traverses the local media list and renames the media
func (a *Application) RenameMedia(list types.LocalMediaList) error {

	doRename, ok := Renamers[viper.GetString("action")]

	if !ok {
		return fmt.Errorf("%s: unknown action", viper.GetString("action"))
	}

	for _, m := range list.List() {
		ctx := log.WithField("media", m).WithField("action", viper.GetString("action"))

		scraped, err := a.scrapeMedia(m)

		if err != nil {
			return err
		}

		if err = m.Merge(scraped); err != nil {
			return err
		}

		if movie, ok := m.TypeMovie(); ok {
			err = a.renameMovie(m, movie, doRename)
		} else if episode, ok := m.TypeEpisode(); ok {
			err = a.renameEpisode(m, episode, doRename)
		} else {
			err = errors.New("unknown media format cannot rename")
		}

		if a.Config().Strict() && err != nil {
			return err
		}

		if err != nil {
			if _, ok := err.(*mediaExistsError); ok {
				ctx.WithField("reason", "media already exists").Warn("Rename skipped")
			} else {
				ctx.WithError(err).Error("Rename failed")
			}
		} else {
			ctx.Info("Media renamed")
		}
	}
	return nil
}

func (a *Application) scrapeMedia(m types.Media) (types.Media, error) {
	for _, s := range a.Scrapers() {
		scraped, err := s.Scrape(m)

		if err != nil {
			if provider.IsErrMediaNotSupported(err) {
				continue
			}
			return nil, err
		}
		return scraped, nil
	}
	return nil, errors.New("no scrapers to use for media")
}

func (a *Application) renameMovie(local types.Local, m types.Movie, rename renamer) error {
	var buf bytes.Buffer
	template := a.Config().Movies().Template()
	if template == nil {
		return errors.New("missing template for movies")
	}
	data := struct {
		Movie   string
		Year    int
		Quality string
		Codec   string
		Group   string
	}{
		Movie:   cleanString(m.MovieName()),
		Year:    m.Year(),
		Quality: m.Quality().String(),
		Codec:   m.Codec().String(),
		Group:   cleanString(m.Group()),
	}
	if err := template.Execute(&buf, &data); err != nil {
		return err
	}
	filename := truncateSpaces(buf.String() + filepath.Ext(local.Name()))
	dest := filepath.Join(a.Config().Movies().Directory(), filename)
	return rename.Rename(local, dest, a.Config().Force())
}

func (a *Application) renameEpisode(local types.Local, e types.Episode, rename renamer) error {
	var buf bytes.Buffer
	template := a.Config().TVShows().Template()
	if template == nil {
		return errors.New("missing template for tvshows")
	}
	data := struct {
		TVShow  string
		Name    string
		Episode int
		Season  int
		Quality string
		Codec   string
		Group   string
	}{
		TVShow:  cleanString(e.TVShow()),
		Name:    cleanString(e.EpisodeName()),
		Episode: e.Episode(),
		Season:  e.Season(),
		Quality: e.Quality().String(),
		Codec:   e.Codec().String(),
		Group:   cleanString(e.Group()),
	}
	if err := template.Execute(&buf, &data); err != nil {
		return err
	}
	filename := truncateSpaces(buf.String() + filepath.Ext(local.Name()))
	dest := filepath.Join(a.Config().TVShows().Directory(), filename)
	return rename.Rename(local, dest, a.Config().Force())
}
