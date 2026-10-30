// Package bootfixture reads the Exchange's boot fixture: a file that declares
// the seats, publishers, sites and listings an Exchange creates at start-up when
// they are absent (ADR-029). An operator points EXCHANGE_BOOT_FIXTURE_FILE at it.
//
// The package reads and checks the file and does nothing else. It decodes
// strictly: an unknown key, a second document, an empty file or a file that
// declares nothing is refused, so a typo cannot quietly leave a row out. It checks
// what the file alone can show: required values, the shape of ids and domains,
// references between entries and duplicates. It reads each listing's terms file
// from the fixture's own directory. Whether the terms build into license terms,
// and what is already stored, are for the Exchange's service to judge when it
// plans and applies the fixture.
package bootfixture

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/internal/cdn"
	"gitlab.postindustria.com/pi-ai/prebid-agentic-content-access/src/exchange/internal/signing"
)

// Version is the fixture format this Exchange reads.
const Version = 1

const (
	// maxFixtureBytes bounds the fixture file, and maxTermsBytes each terms
	// file. Both are operator configuration, far below either bound.
	maxFixtureBytes = 1 << 20
	maxTermsBytes   = 64 << 10
)

// Fixture is a checked boot fixture.
type Fixture struct {
	// Path is the file the fixture was read from, and SHA256 the hex digest of
	// its bytes. The audit rows of what it creates name both.
	Path       string
	SHA256     string
	Seats      []Seat
	Publishers []Publisher
}

// Seat is one Exchange seat: a fora.tenants row.
type Seat struct {
	TenantID      string
	Domain        string
	Ed25519KeyRef string
	// RSAKeyRef is empty for a seat that signs no CloudFront URL. Parse refuses
	// a fixture that lists an AWS_CLOUDFRONT_RSA site on a seat without one.
	RSAKeyRef                  string
	AllowBrokerRelay           bool
	ActivateNewAgentsByDefault bool
}

// Publisher is one payee, a fora.publishers row, and the sites it owns.
type Publisher struct {
	ID          string
	DisplayName string
	Sites       []Site
}

// Site is one publisher site, a fora.publisher_domains row, and the seats it is
// listed on.
type Site struct {
	Domain        string
	SigningScheme signing.Scheme
	// CDN is empty when the fixture records no integration for the site.
	CDN cdn.Provider
	// CloudFrontKeyPairID is set for an AWS_CLOUDFRONT_RSA site and for no other.
	CloudFrontKeyPairID string
	Listings            []Listing
}

// Listing is a site offered on one seat, a fora.publisher_domain_listings row,
// which the Exchange creates ACTIVE.
type Listing struct {
	TenantID string
	// TermsFile is the file the terms were read from, and Terms its bytes: one
	// JSON object, in the shape the console stores a listing's terms in.
	TermsFile        string
	Terms            []byte
	ContentPathScope []string
}

// The file's own shapes, as YAML decodes them. A required boolean is a pointer,
// so a missing one is told apart from false.
type fileFixture struct {
	Version    int             `yaml:"version"`
	Seats      []fileSeat      `yaml:"seats"`
	Publishers []filePublisher `yaml:"publishers"`
}

type fileSeat struct {
	TenantID                   string `yaml:"tenant_id"`
	Domain                     string `yaml:"domain"`
	Ed25519KeyRef              string `yaml:"ed25519_key_ref"`
	RSAKeyRef                  string `yaml:"rsa_key_ref"`
	AllowBrokerRelay           *bool  `yaml:"allow_broker_relay"`
	ActivateNewAgentsByDefault *bool  `yaml:"activate_new_agents_by_default"`
}

type filePublisher struct {
	ID          string     `yaml:"publisher_id"`
	DisplayName string     `yaml:"display_name"`
	Sites       []fileSite `yaml:"sites"`
}

type fileSite struct {
	Domain              string        `yaml:"domain"`
	SigningScheme       string        `yaml:"signing_scheme"`
	CDN                 string        `yaml:"cdn"`
	CloudFrontKeyPairID string        `yaml:"cloudfront_key_pair_id"`
	Listings            []fileListing `yaml:"listings"`
}

type fileListing struct {
	TenantID         string   `yaml:"tenant_id"`
	TermsFile        string   `yaml:"terms_file"`
	ContentPathScope []string `yaml:"content_path_scope"`
}

// Load reads and checks the fixture at path. The fixture and each listing's
// terms file are read from the fixture's own directory through an os.Root, so a
// terms file cannot reach outside that directory, by its name or through a link.
func Load(path string) (Fixture, error) {
	root, err := os.OpenRoot(filepath.Dir(path))
	if err != nil {
		return Fixture{}, fmt.Errorf("open the boot fixture's directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	raw, err := readFile(root.FS(), filepath.Base(path), maxFixtureBytes)
	if err != nil {
		return Fixture{}, fmt.Errorf("read the boot fixture: %w", err)
	}
	f, err := Parse(raw, root.FS())
	if err != nil {
		return Fixture{}, err
	}
	f.Path = path
	return f, nil
}

// Parse checks a fixture's bytes. terms holds the terms files its listings name.
func Parse(raw []byte, terms fs.FS) (Fixture, error) {
	file, err := decode(raw)
	if err != nil {
		return Fixture{}, err
	}
	seats, err := checkSeats(file.Seats)
	if err != nil {
		return Fixture{}, err
	}
	c := checker{seats: make(map[string]Seat, len(seats)), sites: map[string]bool{}, terms: terms}
	for _, s := range seats {
		c.seats[s.TenantID] = s
	}
	publishers, err := c.publishers(file.Publishers)
	if err != nil {
		return Fixture{}, err
	}
	sum := sha256.Sum256(raw)
	return Fixture{SHA256: hex.EncodeToString(sum[:]), Seats: seats, Publishers: publishers}, nil
}

// readFile reads the file name in fsys, refusing one larger than limit bytes.
func readFile(fsys fs.FS, name string, limit int64) ([]byte, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	raw, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", name, limit)
	}
	return raw, nil
}

// decode reads exactly one YAML document, refusing any key the format does not
// name, and checks the format version and that the file declares something.
func decode(raw []byte) (fileFixture, error) {
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	var file fileFixture
	if err := dec.Decode(&file); err != nil {
		if errors.Is(err, io.EOF) {
			return fileFixture{}, errors.New("the boot fixture is empty")
		}
		return fileFixture{}, fmt.Errorf("decode the boot fixture: %w", err)
	}
	var next yaml.Node
	if err := dec.Decode(&next); !errors.Is(err, io.EOF) {
		return fileFixture{}, errors.New("the boot fixture holds more than one YAML document")
	}
	if file.Version != Version {
		return fileFixture{}, fmt.Errorf("version: the boot fixture says %d, and this Exchange reads version %d",
			file.Version, Version)
	}
	if len(file.Seats) == 0 && len(file.Publishers) == 0 {
		return fileFixture{}, errors.New("the boot fixture declares no seat and no publisher")
	}
	return file, nil
}
