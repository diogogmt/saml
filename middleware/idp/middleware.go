// MIT License
//
// Copyright (c) 2017 Pressly Inc.
//
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
//
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
//
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package idp

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/xml"
	"io/ioutil"
	"net/http"
	"text/template"

	"github.com/pressly/saml"
)

// Middleware represents a middleware that provides tools for implementing a
// an identity provider.
type Middleware struct {
	idp *saml.IdentityProvider
}

// NewMiddleware creates a middleware based on the given identity provider.
func NewMiddleware(idp *saml.IdentityProvider) *Middleware {
	if idp == nil {
		panic("IdP cannot be a nil value.")
	}
	return &Middleware{idp: idp}
}

// ServeMetadata generates and serves the IdP's metadata.xml file.
func (m *Middleware) ServeMetadata(w http.ResponseWriter, r *http.Request) {
	metadata, err := m.idp.Metadata()
	if err != nil {
		saml.Logf("Failed to generate metadata: %v", err)
		writeErr(w, err)
		return
	}
	out, err := xml.MarshalIndent(metadata, "", "\t")
	if err != nil {
		saml.Logf("Failed to build metadata: %v", err)
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf8")
	w.Write([]byte("<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n"))
	w.Write(out)
}

// NewLoginRequest creates a login request against an SP.
func (m *Middleware) NewLoginRequest(spMetadataURL string, authFn Authenticator) (*LoginRequest, error) {
	metadata, err := saml.GetMetadata(spMetadataURL)
	if err != nil {
		saml.Logf("Failed to get metadata: %v", err)
		return nil, err
	}
	lr := &LoginRequest{
		spMetadataURL: spMetadataURL,
		m:             m,
		authFn:        authFn,
		metadata:      metadata,
	}
	return lr, nil
}

// ServeSSO creates and serves a SSO assertion based on a request.
func (m *Middleware) ServeSSO(authFn Authenticator) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, err := authFn(w, r)
		if err != nil {
			saml.Logf("authFn: %v", err)
			return
		}

		values := r.URL.Query()

		relayState := values.Get("RelayState")
		samlRequest := values.Get("SAMLRequest")

		data, err := base64.StdEncoding.DecodeString(samlRequest)
		if err != nil {
			saml.Logf("Failed to decode SAMLRequest: %v", err)
			writeErr(w, err)
			return
		}
		buf, err := ioutil.ReadAll(flate.NewReader(bytes.NewBuffer(data)))
		if err != nil {
			saml.Logf("Failed to read SAMLRequest: %v", err)
			writeErr(w, err)
			return
		}

		var authnRequest saml.AuthnRequest
		err = xml.Unmarshal(buf, &authnRequest)
		if err != nil {
			saml.Logf("Failed to unmarshal SAMLRequest: %v", err)
			writeErr(w, err)
			return
		}

		idpAuthnRequest := &saml.IdpAuthnRequest{
			IDP:         m.idp,
			HTTPRequest: r,
			Request:     authnRequest,
		}

		err = idpAuthnRequest.MakeAssertion(sess)
		if err != nil {
			saml.Logf("Failed to make assertion: %v", err)
			writeErr(w, err)
			return
		}

		err = idpAuthnRequest.MarshalAssertion()
		if err != nil {
			saml.Logf("Failed to marshal assertion: %v", err)
			writeErr(w, err)
			return
		}

		err = idpAuthnRequest.MakeResponse()
		if err != nil {
			saml.Logf("Failed to build response: %v", err)
			writeErr(w, err)
			return
		}

		buf, err = xml.MarshalIndent(idpAuthnRequest.Response, "", "\t")
		if err != nil {
			saml.Logf("Failed to format response: %v", err)
			writeErr(w, err)
			return
		}

		form := redirectForm{
			FormAction:   idpAuthnRequest.Assertion.Subject.SubjectConfirmation.SubjectConfirmationData.Recipient,
			RelayState:   relayState, // RelayState is passed as is.
			SAMLResponse: base64.StdEncoding.EncodeToString(buf),
		}

		formTpl, err := template.New("").Parse(redirectFormTemplate)
		if err != nil {
			saml.Logf("Failed to create form: %v", err)
			writeErr(w, err)
			return
		}

		formBuf := bytes.NewBuffer(nil)
		if err := formTpl.Execute(formBuf, form); err != nil {
			saml.Logf("Failed to build form: %v", err)
			writeErr(w, err)
			return
		}

		w.Header().Set("Content-Type", "text/html")
		w.Write(formBuf.Bytes())
	}
}
