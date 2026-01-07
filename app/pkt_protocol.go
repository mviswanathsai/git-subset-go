package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/codecrafters-io/git-starter-go/internal/git"
)

func DemuxNegotiationResponse(br *bufio.Reader, res io.Reader) (*os.File, error) {
	br.Reset(res)
	tmp, err := os.CreateTemp(".", "tmp_pack_")
	if err != nil {
		return nil, fmt.Errorf("Error creating temp pack file: %w", err)
	}
	// Demux the response
	for {
		// 1. Read the 4-byte hex length
		hexLen := make([]byte, 4)
		_, err := io.ReadFull(br, hexLen)
		if err == io.EOF {
			break
		}

		var length int
		fmt.Sscanf(string(hexLen), "%04x", &length)

		if length == 0 { // Flush packet
			continue
		}

		if peek, _ := br.Peek(4); bytes.HasPrefix(peek, []byte("NAK")) {
			br.Discard(length - 4)
			continue
		}
		// 3. Check the first byte (The Channel)
		channel, _ := br.ReadByte()

		switch channel {
		case 1:
			// stream the response body into packfile parsing
			io.CopyN(tmp, br, int64(length-5))
		case 2:
			// This is progress text. Print to stderr.
			io.CopyN(os.Stderr, br, int64(length-5))
		case 3:
			// This is a remote error.
			io.CopyN(os.Stderr, br, int64(length-5))
		}
	}
	return tmp, nil
}

func NegotiateAndReturnResponse(url string, refs map[string][]byte, commonCaps []byte) (io.Reader, error) {
	reqBody, err := prepNegotiationRequest(refs, commonCaps)
	if err != nil {
		return nil, fmt.Errorf("Error preparing request body for negotiation: %w", err)
	}
	negotiationEndpoint := fmt.Sprintf("%s/git-upload-pack", url)
	res, err := http.Post(negotiationEndpoint, git.GitNegotationReqCType, reqBody)
	if err != nil {
		return nil, fmt.Errorf("Negotiation request failed: %w", err)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Negotiation request failed: Unexpected response status code %d", res.StatusCode)
	}
	return res.Body, nil
}

func DiscoverRefs(url string, br *bufio.Reader) (map[string][]byte, []byte, error) {
	str := fmt.Sprintf("%s/info/refs?service=git-upload-pack", url)
	resp, err := http.Get(str)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNotModified {
		return nil, nil, fmt.Errorf("Unexpected status code: %d", resp.StatusCode)
	}

	br.Reset(resp.Body)
	firstFive, err := br.Peek(5)
	if err != nil {
		return nil, nil, err
	}
	if matched, _ := regexp.Match("^[0-9a-f]", firstFive); !matched {
		return nil, nil, fmt.Errorf("Invalid response: Unexpected characters %s at the start of file", firstFive)
	}

	// Read till the first flush packet
	svcName, err := readPktLine(br)
	if err != nil {
		return nil, nil, fmt.Errorf("Error reading pktline: %w", err)
	}

	if !bytes.Equal(svcName, []byte("# service=git-upload-pack")) {
		return nil, nil, fmt.Errorf("Invalid response: Unexpected packet line")
	}

	if _, err := readPktLine(br); err != nil {
		return nil, nil, fmt.Errorf("Error reading pktline: %w", err)
	}

	var commonCaps []byte
	refs := make(map[string][]byte)
	for {
		line, err := readPktLine(br)
		if err != nil {
			return nil, nil, fmt.Errorf("Error reading pktline: %w", err)
		} else if line == nil {
			break
		}
		line, caps, found := bytes.Cut(line, []byte{'\x00'})
		if found {
			var buf bytes.Buffer
			for sCap := range strings.SplitSeq(string(caps), " ") {
				if _, ok := git.C_CAPS[sCap]; ok {
					if _, err := buf.WriteString(sCap); err != nil {
						return nil, nil, err
					}
					if err := buf.WriteByte(' '); err != nil {
						return nil, nil, err
					}
				}
			}
			commonCaps = bytes.TrimSpace(buf.Bytes())
		}
		if bytes.Contains(line, []byte("^{}")) {
			continue
		}
		refs[string(line[41:])] = line[:40]
	}
	return refs, commonCaps, nil
}

func prepNegotiationRequest(refs map[string][]byte, negotiated []byte) (io.Reader, error) {
	var i int
	var buf bytes.Buffer
	for _, pkt := range refs {
		var pktPayload string
		if i == 0 {
			pktPayload = fmt.Sprintf("want %s %s\n", pkt, negotiated)
		} else {
			pktPayload = fmt.Sprintf("want %s\n", pkt)
		}
		pktHeader := fmt.Sprintf("%04x", len(pktPayload)+4)
		if _, err := buf.WriteString(pktHeader); err != nil {
			return nil, err
		}
		if _, err := buf.WriteString(pktPayload); err != nil {
			return nil, err
		}
		i++
	}
	buf.WriteString("0000")
	buf.WriteString("0009done\n")
	return &buf, nil
}

func validateUploadPackResponse(br *bufio.Reader) error {
	firstFive, _ := br.Peek(5)
	if matched, _ := regexp.Match("^[0-9a-f]", firstFive); !matched {
		return fmt.Errorf("Invalid response")
	}

	// Read till the first flush packet
	svcName, _ := readPktLine(br)
	if !bytes.Equal(svcName, []byte("# service=git-upload-pack")) {
		return fmt.Errorf("Invalid response")
	}
	return nil
}

// Return the pktline without any trailing new-line or nul bytes
func readPktLine(r io.Reader) ([]byte, error) {
	pktHeader := make([]byte, 4)
	_, err := io.ReadFull(r, pktHeader)
	if err != nil {
		return nil, err
	}
	pktLength, _ := strconv.ParseInt(string(pktHeader), 16, 32)
	if pktLength == 0 {
		return nil, nil
	}
	out := make([]byte, pktLength-4)
	_, err = io.ReadFull(r, out)
	if err != nil {
		return nil, err
	}
	return bytes.TrimRight(out, "\n\x00"), nil
}
