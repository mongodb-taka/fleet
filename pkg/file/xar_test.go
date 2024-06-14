package file

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCheckPKGSignature(t *testing.T) {
	read := func(name string) []byte {
		b, err := os.ReadFile(name)
		require.NoError(t, err)
		return b
	}
	testCases := []struct {
		in  []byte
		out error
	}{
		{in: []byte{}, out: io.EOF},
		{
			in:  read("./testdata/invalid.tar.gz"),
			out: ErrInvalidType,
		},
		{
			in:  read("./testdata/unsigned.pkg"),
			out: ErrNotSigned,
		},
		{
			in:  read("./testdata/signed.pkg"),
			out: nil,
		},
		{
			out: errors.New("decompressing TOC: unexpected EOF"),
			in:  read("./testdata/wrong-toc.pkg"),
		},
	}

	for _, c := range testCases {
		r := bytes.NewReader(c.in)
		err := CheckPKGSignature(r)
		if c.out != nil {
			require.ErrorContains(t, err, c.out.Error())
		} else {
			require.NoError(t, err)
		}
	}
}

func TestParseDistributionFile(t *testing.T) {
	tests := []struct {
		name             string
		rawXML           []byte
		expectedName     string
		expectedVersion  string
		expectedBundleID string
	}{
		{
			name: "BundleVersionPath",
			rawXML: []byte(`
			<distribution>
				<bundle-version>
					<bundle path="com.example.bundle"/>
				</bundle-version>
			</distribution>`),
			expectedName: "com.example.bundle",
		},
		{
			name: "PkgRefBundleVersionPath",
			rawXML: []byte(`
			<distribution>
				<pkg-ref>
					<bundle-version>
						<bundle path="com.example.pkg.bundle"/>
					</bundle-version>
				</pkg-ref>
			</distribution>`),
			expectedName: "com.example.pkg.bundle",
		},
		{
			name: "Title",
			rawXML: []byte(`
			<distribution>
				<title>Example Title</title>
			</distribution>`),
			expectedName: "Example Title",
		},
		{
			name: "ProductID",
			rawXML: []byte(`
			<distribution>
				<product id="com.example.product"/>
			</distribution>`),
			expectedName:     "com.example.product",
			expectedBundleID: "com.example.product",
		},
		{
			name: "PkgRefID",
			rawXML: []byte(`
			<distribution>
				<pkg-ref id="com.example.pkg"/>
			</distribution>`),
			expectedName:     "com.example.pkg",
			expectedBundleID: "com.example.pkg",
		},
		{
			name: "MustCloseAppID",
			rawXML: []byte(`
			<distribution>
				<must-close>
					<app id="com.example.app"/>
				</must-close>
			</distribution>`),
			expectedBundleID: "com.example.app",
		},
		{
			name: "PkgRefMustCloseAppID",
			rawXML: []byte(`
			<distribution>
				<pkg-ref>
					<must-close>
						<app id="com.example.pkg.app"/>
					</must-close>
				</pkg-ref>
			</distribution>`),
			expectedBundleID: "com.example.pkg.app",
		},
		{
			name: "ProductVersion",
			rawXML: []byte(`
			<distribution>
				<product version="1.2.3"/>
			</distribution>`),
			expectedVersion: "1.2.3",
		},
		{
			name: "PkgRefVersion",
			rawXML: []byte(`
			<distribution>
				<pkg-ref version="4.5.6"/>
			</distribution>`),
			expectedVersion: "4.5.6",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata, err := parseDistributionFile(tt.rawXML)
			require.NoError(t, err)
			require.Equal(t, tt.expectedName, metadata.Name)
			require.Equal(t, tt.expectedVersion, metadata.Version)
			require.Equal(t, tt.expectedBundleID, metadata.BundleIdentifier)
		})
	}
}

func TestParseRealDistributionFiles(t *testing.T) {
	tests := []struct {
		name             string
		file             string
		expectedName     string
		expectedVersion  string
		expectedBundleID string
	}{
		{
			name:             "Microsoft Edge",
			file:             "distribution-edge.xml",
			expectedName:     "Microsoft Edge",
			expectedVersion:  "",
			expectedBundleID: "com.microsoft.edgemac",
		},
		{
			name:             "Zoom",
			file:             "distribution-zoom.xml",
			expectedName:     "Microsoft Edge",
			expectedVersion:  "",
			expectedBundleID: "com.microsoft.edgemac",
		},
		{
			name:             "Chrome",
			file:             "distribution-chrome.xml",
			expectedName:     "Microsoft Edge",
			expectedVersion:  "",
			expectedBundleID: "com.microsoft.edgemac",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawXML, err := os.ReadFile(filepath.Join("testdata", "distribution", tt.file))
			require.NoError(t, err)
			metadata, err := parseDistributionFile(rawXML)
			require.NoError(t, err)
			fmt.Printf("name: %s, version: %s, identifier: %s\n", metadata.Name, metadata.Version, metadata.BundleIdentifier)
			//	require.Equal(t, tt.expectedName, metadata.Name)
			//	require.Equal(t, tt.expectedVersion, metadata.Version)
			//	require.Equal(t, tt.expectedBundleID, metadata.BundleIdentifier)
		})
	}
}
