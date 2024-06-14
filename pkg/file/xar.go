package file

//		Copyright 2023 SAS Software
//
//	 Licensed under the Apache License, Version 2.0 (the "License");
//	 you may not use this file except in compliance with the License.
//	 You may obtain a copy of the License at
//
//	     http://www.apache.org/licenses/LICENSE-2.0
//
//	 Unless required by applicable law or agreed to in writing, software
//	 distributed under the License is distributed on an "AS IS" BASIS,
//	 WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
//	 See the License for the specific language governing permissions and
//	 limitations under the License.
//
// xar contains utilities to parse xar files, most of the logic here is a
// simplified version extracted from the logic to sign xar files in
// https://github.com/sassoftware/relic

import (
	"bytes"
	"compress/bzip2"
	"compress/zlib"
	"crypto"
	"crypto/sha256"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

const (
	// xarMagic is the [file signature][1] (or magic bytes) for xar
	//
	// [1]: https://en.wikipedia.org/wiki/List_of_file_signatures
	xarMagic = 0x78617221

	xarHeaderSize = 28
)

const (
	hashNone uint32 = iota
	hashSHA1
	hashMD5
	hashSHA256
	hashSHA512
)

var (
	// ErrInvalidType is used to signal that the provided package can't be
	// parsed because is an invalid file type.
	ErrInvalidType = errors.New("invalid file type")
	// ErrNotSigned is used to signal that the provided package doesn't
	// contain a signature.
	ErrNotSigned = errors.New("file is not signed")
)

type xarHeader struct {
	Magic            uint32
	HeaderSize       uint16
	Version          uint16
	CompressedSize   int64
	UncompressedSize int64
	HashType         uint32
}

type tocXar struct {
	TOC toc `xml:"toc"`
}

type toc struct {
	Signature  *any `xml:"signature"`
	XSignature *any `xml:"x-signature"`
}

type xmlXar struct {
	XMLName xml.Name `xml:"xar"`
	TOC     xmlTOC
}

type xmlTOC struct {
	XMLName xml.Name   `xml:"toc"`
	Files   []*xmlFile `xml:"file"`
}

type xmlFileData struct {
	XMLName  xml.Name `xml:"data"`
	Length   int64    `xml:"length"`
	Offset   int64    `xml:"offset"`
	Size     int64    `xml:"size"`
	Encoding struct {
		Style string `xml:"style,attr"`
	} `xml:"encoding"`
}

type xmlFile struct {
	XMLName xml.Name `xml:"file"`
	Name    string   `xml:"name"`
	Data    *xmlFileData
}

// distributionXML represents the structure of the distributionXML.xml
type distributionXML struct {
	Title          string                      `xml:"title"`
	Product        distributionProduct         `xml:"product"`
	PkgRefs        []distributionPkgRef        `xml:"pkg-ref"`
	BundleVersions []distributionBundleVersion `xml:"bundle-version"`
	MustClose      distributionMustClose       `xml:"must-close"`
}

// distributionProduct represents the product element
type distributionProduct struct {
	ID      string `xml:"id,attr"`
	Version string `xml:"version,attr"`
}

// distributionPkgRef represents the pkg-ref element
type distributionPkgRef struct {
	ID             string                      `xml:"id,attr"`
	Version        string                      `xml:"version,attr"`
	BundleVersions []distributionBundleVersion `xml:"bundle-version"`
	MustClose      distributionMustClose       `xml:"must-close"`
}

// distributionBundleVersion represents the bundle-version element
type distributionBundleVersion struct {
	Bundles []distributionBundle `xml:"bundle"`
}

// distributionBundle represents the bundle element
type distributionBundle struct {
	Path string `xml:"path,attr"`
}

// distributionMustClose represents the must-close element
type distributionMustClose struct {
	Apps []distributionApp `xml:"app"`
}

// distributionApp represents the app element
type distributionApp struct {
	ID string `xml:"id,attr"`
}

// ExtractXARMetadata extracts the name and version metadata from a .pkg file
// in the XAR format.
func ExtractXARMetadata(r io.Reader) (*InstallerMetadata, error) {
	var hdr xarHeader

	h := sha256.New()
	r = io.TeeReader(r, h)
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, fmt.Errorf("failed to read all content: %w", err)
	}

	rr := bytes.NewReader(b)
	if err := binary.Read(rr, binary.BigEndian, &hdr); err != nil {
		return nil, fmt.Errorf("decode xar header: %w", err)
	}

	zr, err := zlib.NewReader(io.LimitReader(rr, hdr.CompressedSize))
	if err != nil {
		return nil, fmt.Errorf("create zlib reader: %w", err)
	}
	defer zr.Close()

	var root xmlXar
	decoder := xml.NewDecoder(zr)
	decoder.Strict = false
	if err := decoder.Decode(&root); err != nil {
		return nil, fmt.Errorf("decode xar xml: %w", err)
	}

	heapOffset := xarHeaderSize + hdr.CompressedSize
	for _, f := range root.TOC.Files {
		if f.Name == "Distribution" {
			var fileReader io.Reader
			heapReader := io.NewSectionReader(rr, heapOffset, int64(len(b))-heapOffset)
			fileReader = io.NewSectionReader(heapReader, f.Data.Offset, f.Data.Length)

			// the distribution file can be compressed differently than the TOC, the
			// actual compression is specified in the Encoding.Style field.
			if strings.Contains(f.Data.Encoding.Style, "x-gzip") {
				// despite the name, x-gzip fails to decode with the gzip package
				// (invalid header), but it works with zlib.
				zr, err := zlib.NewReader(fileReader)
				if err != nil {
					return nil, fmt.Errorf("create zlib reader: %w", err)
				}
				defer zr.Close()
				fileReader = zr
			} else if strings.Contains(f.Data.Encoding.Style, "x-bzip2") {
				fileReader = bzip2.NewReader(fileReader)
			}
			// TODO: what other compression methods are supported?

			contents, err := io.ReadAll(fileReader)
			if err != nil {
				return nil, fmt.Errorf("reading Distribution file: %w", err)
			}

			meta, err := parseDistributionFile(contents)
			if err != nil {
				return nil, fmt.Errorf("parsing Distribution file: %w", err)
			}
			meta.SHASum = h.Sum(nil)
			return meta, err

			//	var distXML distributionXML
			//	if err := xml.Unmarshal(contents, &distXML); err != nil {
			//		return nil, fmt.Errorf("unmarshal Distribution XML: %w", err)
			//	}

			// Most of the following requirements/order of
			// priorities described below has been [specified by
			// the product team][1] please be mindful when changing them.
			// [1]: https://github.com/fleetdm/fleet/issues/19144,

			//			name := strings.TrimSpace(distXML.Title)
			//			if name == "" {
			//				name = strings.TrimSpace(distXML.Product.ID)
			//			}
			//			version := strings.TrimSpace(distXML.Product.Version)
			//			if len(distXML.PkgRef) > 0 {
			//				if name == "" {
			//					name = strings.TrimSpace(distXML.PkgRef[0].ID)
			//				}
			//				if version == "" {
			//					version = strings.TrimSpace(distXML.PkgRef[0].Version)
			//				}
			//				return &InstallerMetadata{
			//					Name:    name,
			//					Version: version,
			//					SHASum:  h.Sum(nil),
			//				}, nil
			//			}

		}
	}

	return &InstallerMetadata{SHASum: h.Sum(nil)}, nil
}

func parseDistributionFile(rawXML []byte) (*InstallerMetadata, error) {
	var distXML distributionXML
	if err := xml.Unmarshal(rawXML, &distXML); err != nil {
		return nil, fmt.Errorf("unmarshal Distribution XML: %w", err)
	}

	return &InstallerMetadata{
		Name:             getName(&distXML),
		Version:          getVersion(&distXML),
		BundleIdentifier: getBundleIdentifier(&distXML),
	}, nil

}

// Get the name from (in order of priority):
// - bundle-version[0].bundle[0].path (extract from first <bundle-version> -> <bundle> -> path attribute)
// - title
// - product.id
// - pkg-ref[0].id
func getName(d *distributionXML) string {
	if len(d.BundleVersions) > 0 {
		return d.BundleVersions[0].Bundles[0].Path
	}
	//	for _, pkg := range d.PkgRefs {
	//		if len(pkg.BundleVersions) > 0 && len(pkg.BundleVersions[0].Bundles) > 0 {
	//			return pkg.BundleVersions[0].Bundles[0].Path
	//		}
	//	}
	if d.Title != "" {
		return d.Title
	}
	if d.Product.ID != "" {
		return d.Product.ID
	}
	if len(d.PkgRefs) > 0 {
		return d.PkgRefs[0].ID
	}
	return ""
}

// Get the bundle identifier from (in order of priority):
// - must-close[0].app[0].id extract from first <must-close> -> <app> -> id attribute)
// - product.id
// - pkg-ref[0].id
func getBundleIdentifier(d *distributionXML) string {
	if len(d.MustClose.Apps) > 0 {
		return d.MustClose.Apps[0].ID
	}
	//	for _, pkg := range d.PkgRefs {
	//		if len(pkg.MustClose.Apps) > 0 {
	//			return pkg.MustClose.Apps[0].ID
	//		}
	//	}
	if d.Product.ID != "" {
		return d.Product.ID
	}
	if len(d.PkgRefs) > 0 {
		return d.PkgRefs[0].ID
	}
	return ""
}

// Get the version from (in order of priority):
// - product.version
// - pkg-ref[0].version
func getVersion(d *distributionXML) string {
	if d.Product.Version != "" {
		return d.Product.Version
	}
	if len(d.PkgRefs) > 0 {
		return d.PkgRefs[0].Version
	}
	return ""
}

// CheckPKGSignature checks if the provided bytes correspond to a signed pkg
// (xar) file.
//
// - If the file is not xar, it returns a ErrInvalidType error
// - If the file is not signed, it returns a ErrNotSigned error
func CheckPKGSignature(pkg io.Reader) error {
	buff := bytes.NewBuffer(nil)
	if _, err := io.Copy(buff, pkg); err != nil {
		return err
	}
	r := bytes.NewReader(buff.Bytes())

	hdr, hashType, err := parseHeader(io.NewSectionReader(r, 0, 28))
	if err != nil {
		return err
	}

	base := int64(hdr.HeaderSize)
	toc, err := parseTOC(io.NewSectionReader(r, base, hdr.CompressedSize), hashType)
	if err != nil {
		return err
	}

	if toc.Signature == nil && toc.XSignature == nil {
		return ErrNotSigned
	}

	return nil
}

func decompress(r io.Reader) ([]byte, error) {
	zr, err := zlib.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

func parseTOC(r io.Reader, hashType crypto.Hash) (*toc, error) {
	tocHash := hashType.New()
	r = io.TeeReader(r, tocHash)
	decomp, err := decompress(r)
	if err != nil {
		return nil, fmt.Errorf("decompressing TOC: %w", err)
	}
	var toc tocXar
	if err := xml.Unmarshal(decomp, &toc); err != nil {
		return nil, fmt.Errorf("decoding TOC: %w", err)
	}
	return &toc.TOC, nil
}

func parseHeader(r io.Reader) (xarHeader, crypto.Hash, error) {
	var hdr xarHeader
	if err := binary.Read(r, binary.BigEndian, &hdr); err != nil {
		return xarHeader{}, 0, err
	}

	if hdr.Magic != xarMagic {
		return hdr, 0, ErrInvalidType
	}

	var hashType crypto.Hash
	switch hdr.HashType {
	case hashSHA1:
		hashType = crypto.SHA1
	case hashSHA256:
		hashType = crypto.SHA256
	case hashSHA512:
		hashType = crypto.SHA512
	default:
		return xarHeader{}, 0, fmt.Errorf("unknown hash algorithm %d", hdr.HashType)
	}

	return hdr, hashType, nil
}
