package transport

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	utls "github.com/refraction-networking/utls"
)

type (
	Ja3 struct {
		Ja3       string
		UserAgent string
		Hash      string
	}
)

// greasePlaceholder is a random value (well, kindof '0x?a?a) specified in a
// random RFC.
const greasePlaceholder = 0x0a0a

// ErrExtensionNotExist is returned when an extension is not supported by the library
type ErrExtensionNotExist string

// Error is the error value which contains the extension that does not exist
func (e ErrExtensionNotExist) Error() string {
	return fmt.Sprintf("Extension does not exist: %s\n", string(e))
}

// / extMap maps extension values to the TLSExtension object associated with the
// number. Some values are not put in here because they must be applied in a
// special way. For example, "10" is the SupportedCurves extension which is also
// used to calculate the JA3 signature. These JA3-dependent values are applied
// after the instantiation of the map.
// https://www.iana.org/assignments/tls-extensiontype-values/tls-extensiontype-values.xhtml
func newExtMap() map[string]utls.TLSExtension {
	return map[string]utls.TLSExtension{
		"0": &utls.SNIExtension{},
		"5": &utls.StatusRequestExtension{},
		// These are applied later
		// "10": &tls.SupportedCurvesExtension{...}
		// "11": &tls.SupportedPointsExtension{...}
		"13": &utls.SignatureAlgorithmsExtension{
			SupportedSignatureAlgorithms: []utls.SignatureScheme{
				utls.ECDSAWithP256AndSHA256,
				utls.ECDSAWithP384AndSHA384,
				utls.ECDSAWithP521AndSHA512,
				utls.PSSWithSHA256,
				utls.PSSWithSHA384,
				utls.PSSWithSHA512,
				utls.PKCS1WithSHA256,
				utls.PKCS1WithSHA384,
				utls.PKCS1WithSHA512,
				utls.ECDSAWithSHA1,
				utls.PKCS1WithSHA1,
			},
		},
		"16": &utls.ALPNExtension{
			AlpnProtocols: []string{"h2", "http/1.1"},
		},
		"18": &utls.SCTExtension{},
		"21": &utls.UtlsPaddingExtension{GetPaddingLen: utls.BoringPaddingStyle},
		"22": &utls.GenericExtension{Id: 22}, // encrypt_then_mac
		//"22": &utls.GenericExtension{Id: 0x16, Data: []uint8{}},
		"23": &utls.ExtendedMasterSecretExtension{},
		//"27": &utls.FakeCertCompressionAlgsExtension{},
		"27": &utls.UtlsCompressCertExtension{
			Algorithms: []utls.CertCompressionAlgo{utls.CertCompressionBrotli},
		},
		"28": &utls.FakeRecordSizeLimitExtension{}, //Limit: 0x4001
		"35": &utls.SessionTicketExtension{},
		"34": &utls.GenericExtension{Id: 34},
		"41": &utls.GenericExtension{Id: 41}, //FIXME pre_shared_key
		"43": &utls.SupportedVersionsExtension{Versions: []uint16{
			// utls.GREASE_PLACEHOLDER, //可能导致版本错乱
			// utls.VersionTLS13, // NOTE 不想支持的加上去会报错
			utls.VersionTLS12,
			utls.VersionTLS11,
			utls.VersionTLS10}},
		"44": &utls.CookieExtension{},
		"45": &utls.PSKKeyExchangeModesExtension{
			Modes: []uint8{utls.PskModeDHE},
		},
		"49": &utls.GenericExtension{Id: 49}, // post_handshake_auth
		"50": &utls.GenericExtension{Id: 50}, // signature_algorithms_cert
		//"51": &utls.KeyShareExtension{KeyShares: []utls.KeyShare{},},
		"51": &utls.KeyShareExtension{KeyShares: []utls.KeyShare{
			//	{Group: utls.GREASE_PLACEHOLDER, Data: []byte{0}}, //可能导致版本错乱
			{Group: utls.X25519},
			{Group: utls.CurveP256},
			{Group: utls.CurveP384},
			//{Group: utls.CurveP521},

			// {Group: utls.CurveP384}, known bug missing correct extensions for handshake
		}},
		"30032": &utls.GenericExtension{Id: 0x7550, Data: []byte{0}}, //FIXME
		"13172": &utls.NPNExtension{},
		"65281": &utls.RenegotiationInfoExtension{
			Renegotiation: utls.RenegotiateOnceAsClient,
		},
	}
}

func extsMapping(token string) utls.TLSExtension {
	switch token {
	case "0":
		return &utls.SNIExtension{}
	case "5":
		return &utls.StatusRequestExtension{}
		// These are applied later
		// "10": &tls.SupportedCurvesExtension{...}
		// "11": &tls.SupportedPointsExtension{...}
	case "13":
		return &utls.SignatureAlgorithmsExtension{
			SupportedSignatureAlgorithms: []utls.SignatureScheme{
				utls.ECDSAWithP256AndSHA256,
				utls.ECDSAWithP384AndSHA384,
				utls.ECDSAWithP521AndSHA512,
				utls.PSSWithSHA256,
				utls.PSSWithSHA384,
				utls.PSSWithSHA512,
				utls.PKCS1WithSHA256,
				utls.PKCS1WithSHA384,
				utls.PKCS1WithSHA512,
				utls.ECDSAWithSHA1,
				utls.PKCS1WithSHA1,
			},
		}
	case "16":
		return &utls.ALPNExtension{
			AlpnProtocols: []string{"h2", "http/1.1"},
		}
	case "18":
		return &utls.SCTExtension{}
	case "21":
		return &utls.UtlsPaddingExtension{GetPaddingLen: utls.BoringPaddingStyle}
	case "22":
		return &utls.GenericExtension{Id: 22} // encrypt_then_mac
		//case "22": &utls.GenericExtension{Id: 0x16, Data: []uint8{}},
	case "23":
		return &utls.ExtendedMasterSecretExtension{}
		//"27": &utls.FakeCertCompressionAlgsExtension{},
	case "27":
		return &utls.UtlsCompressCertExtension{
			Algorithms: []utls.CertCompressionAlgo{utls.CertCompressionBrotli},
		}
	case "28":
		return &utls.FakeRecordSizeLimitExtension{} //Limit: 0x4001
	case "35":
		return &utls.SessionTicketExtension{}
	case "34":
		return &utls.GenericExtension{Id: 34}
	case "41":
		return &utls.GenericExtension{Id: 41} //FIXME pre_shared_key
	case "43":
		return &utls.SupportedVersionsExtension{Versions: []uint16{
			// utls.GREASE_PLACEHOLDER, //可能导致版本错乱
			// utls.VersionTLS13, // NOTE 不想支持的加上去会报错
			utls.VersionTLS12,
			utls.VersionTLS11,
			utls.VersionTLS10}}
	case "44":
		return &utls.CookieExtension{}
	case "45":
		return &utls.PSKKeyExchangeModesExtension{
			Modes: []uint8{utls.PskModeDHE},
		}
	case "49":
		return &utls.GenericExtension{Id: 49} // post_handshake_auth
	case "50":
		return &utls.GenericExtension{Id: 50} // signature_algorithms_cert
		//"51": &utls.KeyShareExtension{KeyShares: []utls.KeyShare{},},
	case "51":
		return &utls.KeyShareExtension{KeyShares: []utls.KeyShare{
			//	{Group: utls.GREASE_PLACEHOLDER, Data: []byte{0}}, //可能导致版本错乱
			{Group: utls.X25519},
			{Group: utls.CurveP256},
			{Group: utls.CurveP384},
			//{Group: utls.CurveP521},

			// {Group: utls.CurveP384}, known bug missing correct extensions for handshake
		}}
	case "30032":
		return &utls.GenericExtension{Id: 0x7550, Data: []byte{0}} //FIXME
	case "13172":
		return &utls.NPNExtension{}
	case "65281":
		return &utls.RenegotiationInfoExtension{
			Renegotiation: utls.RenegotiateOnceAsClient,
		}
	}

	return nil
}

// parseJA3 creates a ClientHelloSpec based on a JA3 string
func parseJA3(ja3 string) (*utls.ClientHelloSpec, error) {
	//tmpMap := newExtMap()
	tokens := strings.Split(ja3, ",")

	//version := tokens[0]
	ciphers := strings.Split(tokens[1], "-")
	extensions := strings.Split(tokens[2], "-")
	curves := strings.Split(tokens[3], "-")
	if len(curves) == 1 && curves[0] == "" {
		curves = []string{}
	}
	pointFormats := strings.Split(tokens[4], "-")
	if len(pointFormats) == 1 && pointFormats[0] == "" {
		pointFormats = []string{}
	}

	// parse curves
	var targetCurves []utls.CurveID
	targetCurves = append(targetCurves, utls.CurveID(utls.CurveID(utls.GREASE_PLACEHOLDER))) //append grease for Chrome browsers
	for _, c := range curves {
		cid, err := strconv.ParseUint(c, 10, 16)
		if err != nil {
			return nil, err
		}
		targetCurves = append(targetCurves, utls.CurveID(cid))
	}
	//tmpMap["10"] = &utls.SupportedCurvesExtension{Curves: targetCurves}

	// parse point formats
	var targetPointFormats []byte
	for _, p := range pointFormats {
		pid, err := strconv.ParseUint(p, 10, 8)
		if err != nil {
			return nil, err
		}
		targetPointFormats = append(targetPointFormats, byte(pid))
	}
	//tmpMap["11"] = &utls.SupportedPointsExtension{SupportedPoints: targetPointFormats}

	// build extenions list
	var exts []utls.TLSExtension
	for _, e := range extensions {
		te := extsMapping(e)
		if te == nil {
			if e == "10" {
				te = &utls.SupportedCurvesExtension{Curves: targetCurves}
			}
			if e == "11" {
				te = &utls.SupportedPointsExtension{SupportedPoints: targetPointFormats}
			}

			if te == nil {
				return nil, ErrExtensionNotExist(e)
			}
		}
		//te, ok := tmpMap[e]
		//if !ok {
		//	return nil, ErrExtensionNotExist(e)
		//}
		exts = append(exts, te)
	}
	// build SSLVersion
	//vid64, err := strconv.ParseUint(version, 10, 16)
	//if err != nil {
	//	return nil, err
	//}
	//vid := uint16(vid64)

	// build CipherSuites
	var suites []uint16
	for _, c := range ciphers {
		cid, err := strconv.ParseUint(c, 10, 16)
		if err != nil {
			return nil, err
		}
		suites = append(suites, uint16(cid))
	}

	return &utls.ClientHelloSpec{
		//TLSVersMin:         vid,
		//TLSVersMax:         vid,
		CipherSuites:       suites,
		CompressionMethods: []byte{0},
		Extensions:         exts,
		GetSessionID:       sha256.Sum256,
	}, nil
}

func urlToHost(target *url.URL) *url.URL {
	if !strings.Contains(target.Host, ":") {
		if target.Scheme == "http" {
			target.Host = target.Host + ":80"
		} else if target.Scheme == "https" {
			target.Host = target.Host + ":443"
		}
	}
	return target
}
