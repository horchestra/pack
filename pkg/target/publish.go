package target

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"slices"
	"strings"
)

// Publication is a value a job published: the file /var/run/pack/publish/<Name>/<Key>, which a pod reads as
// ${secret:<Name>/<Key>}.
type Publication struct{ Name, Key, Value string }

// Published reads a tar of the publish directory, publish/<name>/<key>: a deeper path, or a dotted name or key, is
// the job's own and is not published.
func Published(archive []byte) ([]Publication, error) {
	var out []Publication
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		segs := strings.Split(strings.TrimPrefix(h.Name, "./"), "/")
		if len(segs) != 3 || slices.ContainsFunc(segs[1:], func(s string) bool { return len(s) == 0 || s[0] == '.' }) {
			continue
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			return nil, err
		}
		out = append(out, Publication{segs[1], segs[2], string(data)})
	}
}
