// Package rsl reads a publisher's RSL 1.0 licensing document and maps the rule
// covering one page onto FORA license terms.
//
// RSL is a vocabulary source for FORA, not a format FORA speaks: the protocol
// seeds its function tokens from RSL's AI-use terms and names rsl.txt as a
// catalog source, but defines no RSL document of its own. This package is that
// one-way bridge. It reads what a site already publishes so a publisher sees
// their real terms in a preview, rather than terms someone typed again.
//
// What is read: the content rules, their licenses, the permitted and prohibited
// tokens on each axis, and the payment. The warranty and disclaimer statements
// are not read — they are a legal surface FORA has no field for, and inventing
// a mapping for them would put words in a publisher's mouth.
package rsl

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrNotRSL is returned when the document's root element is not <rsl>. An
// origin that answers /rsl.txt with its home page lands here, which is the
// common case and not an error worth alarming about.
var ErrNotRSL = errors.New("rsl: the document is not an RSL document")

// Document is a parsed RSL document.
type Document struct {
	Contents []Content
}

// Content is one rule: an address pattern and the licenses offered for it.
type Content struct {
	// URL is the address pattern as written, e.g. "/", "/news/*" or an
	// absolute address.
	URL string
	// Licenses are the license offers for this address, in document order.
	Licenses []License
}

// License is one offer within a content rule.
type License struct {
	// Permits and Prohibits carry the tokens per axis, one entry per axis the
	// document names.
	Permits   []Usage
	Prohibits []Usage
	// Payment is what the publisher charges. Absent means the document states
	// no price, which RSL reads as "ask the publisher".
	Payment *Payment
}

// Usage is one axis of a permits or prohibits statement.
type Usage struct {
	// Type is the axis: "usage", "user" or "geo". An empty value means the
	// document omitted it, which RSL reads as "usage".
	Type string
	// Tokens are the values, already split on whitespace.
	Tokens []string
}

// Payment is an RSL payment statement.
type Payment struct {
	// Type is the RSL payment type: free, attribution, contribution,
	// subscription, crawl, training, use, inference or purchase.
	Type string
	// Amounts are the declared prices. RSL allows several; the first is the
	// one a mapping reads.
	Amounts []Amount
	// Standard and Custom address the licensing document behind this payment.
	Standard string
	Custom   string
}

// Amount is one declared price.
type Amount struct {
	Currency string
	Value    string
}

// Parse reads an RSL document from r.
//
// Element names are matched without their namespace: documents in the wild
// declare the RSL namespace as the default, as a prefix, or leave it out, and
// all three are the same document. Elements this package does not read are
// skipped rather than refused, so a document carrying warranties, attestations
// or a payment protocol still yields its terms.
func Parse(r io.Reader) (*Document, error) {
	var raw rawDocument
	dec := xml.NewDecoder(r)
	dec.Entity = xml.HTMLEntity
	if err := dec.Decode(&raw); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("%w: the document is empty", ErrNotRSL)
		}
		// Go reports a root-element mismatch as a plain error; both that and a
		// malformed document mean the same thing to a caller, which is that
		// there are no terms to read here.
		return nil, fmt.Errorf("%w: %w", ErrNotRSL, err)
	}
	doc := &Document{}
	for _, rc := range raw.Contents {
		content := Content{URL: strings.TrimSpace(rc.URL)}
		for _, rl := range rc.Licenses {
			content.Licenses = append(content.Licenses, rl.toLicense())
		}
		doc.Contents = append(doc.Contents, content)
	}
	return doc, nil
}

// rawDocument mirrors the XML shape. It is separate from the exported types so
// the public surface carries split token lists rather than the raw text
// content, and so an XML detail cannot leak into a caller's switch.
type rawDocument struct {
	XMLName  xml.Name     `xml:"rsl"`
	Contents []rawContent `xml:"content"`
}

type rawContent struct {
	URL      string       `xml:"url,attr"`
	Licenses []rawLicense `xml:"license"`
}

type rawLicense struct {
	Permits   []rawUsage  `xml:"permits"`
	Prohibits []rawUsage  `xml:"prohibits"`
	Payment   *rawPayment `xml:"payment"`
}

type rawUsage struct {
	Type   string `xml:"type,attr"`
	Tokens string `xml:",chardata"`
}

type rawPayment struct {
	Type     string      `xml:"type,attr"`
	Amounts  []rawAmount `xml:"amount"`
	Standard string      `xml:"standard"`
	Custom   string      `xml:"custom"`
}

type rawAmount struct {
	Currency string `xml:"currency,attr"`
	Value    string `xml:",chardata"`
}

func (rl rawLicense) toLicense() License {
	l := License{}
	for _, u := range rl.Permits {
		l.Permits = append(l.Permits, u.toUsage())
	}
	for _, u := range rl.Prohibits {
		l.Prohibits = append(l.Prohibits, u.toUsage())
	}
	if rl.Payment != nil {
		p := Payment{
			Type:     strings.ToLower(strings.TrimSpace(rl.Payment.Type)),
			Standard: strings.TrimSpace(rl.Payment.Standard),
			Custom:   strings.TrimSpace(rl.Payment.Custom),
		}
		for _, a := range rl.Payment.Amounts {
			p.Amounts = append(p.Amounts, Amount{
				Currency: strings.TrimSpace(a.Currency),
				Value:    strings.TrimSpace(a.Value),
			})
		}
		l.Payment = &p
	}
	return l
}

func (u rawUsage) toUsage() Usage {
	return Usage{
		Type:   strings.ToLower(strings.TrimSpace(u.Type)),
		Tokens: strings.Fields(u.Tokens),
	}
}
