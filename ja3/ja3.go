package ja3

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"

	utls "github.com/refraction-networking/utls"
)

// JA3 串 → ClientHelloSpec 的重建。
//
// **这条路是近似的，能用预置名就用预置名**（见 profile.go）。留着它是为了向后
// 兼容：抓包工具吐出来的是 JA3 串，网上流传的指纹库也是 JA3 串。
//
// JA3 的五段：
//
//	TLSVersion, Ciphers, Extensions, EllipticCurves, ECPointFormats
//	771,        4865-…,  0-23-…,     29-23-24,       0
//
// 第三段**只有扩展 id**，没有内容 —— 所以每个 id 对应的扩展内容由本文件按"真实
// 浏览器通常怎么填"补出来。补得再像也只是补的：一个 JA3 串重建出来的 ClientHello
// 与它抓自的那个浏览器**逐字节不同**。

// ErrExtensionNotExist 表示 JA3 串里有一个本包不认识的扩展 id。
//
// ★ 明确报错而不是跳过：跳过会得到一个"少了一个扩展"的指纹 —— 它不再是串里描述
// 的那个客户端，而调用方以为自己伪装成功了。
type ErrExtensionNotExist string

func (e ErrExtensionNotExist) Error() string {
	return fmt.Sprintf("ja3: 不支持的扩展 id %q（这条 JA3 串重建不出来，改用预置名）", string(e))
}

// isGREASE 判断一个值是不是 GREASE（RFC 8701）。
//
// GREASE 值形如 0x?A?A，且高低字节相同：0x0A0A、0x1A1A … 0xFAFA。浏览器往密码
// 套件、扩展、曲线、supported_versions 里各塞一个，用来检验中间设备容不容得下
// 未知值。
//
// ★ 标准的 JA3 计算**会把 GREASE 全部滤掉**，所以多数串里根本没有这一维；
// 但有些抓包工具原样保留。识别出来换成 utls 的占位符，utls 会在握手时填一个
// 随机的合法 GREASE 值 —— 直接把 0x0A0A 当成真实的曲线/套件发出去，反倒成了
// 一个"永远发同一个 GREASE"的独特指纹。
func isGREASE(v uint16) bool {
	return v&0x0f0f == 0x0a0a && byte(v>>8) == byte(v)
}

// parseJA3 按 JA3 串重建 ClientHelloSpec。
func parseJA3(ja3 string) (*utls.ClientHelloSpec, error) {
	tokens := strings.Split(ja3, ",")
	if len(tokens) != 5 {
		return nil, fmt.Errorf("ja3: JA3 串应为 5 段，实际 %d 段", len(tokens))
	}

	b := &builder{}
	var err error

	if b.curves, b.greaseCurve, err = parseCurves(tokens[3]); err != nil {
		return nil, err
	}
	if b.pointFormats, err = parsePointFormats(tokens[4]); err != nil {
		return nil, err
	}

	suites, err := parseCiphers(tokens[1])
	if err != nil {
		return nil, err
	}

	extTokens := splitList(tokens[2])
	// supported_versions（43）在不在，决定这是个 TLS 1.3 客户端还是 1.2 的。
	// 得先知道，因为 key_share 只在 1.3 下有意义。
	for _, e := range extTokens {
		if e == "43" {
			b.tls13 = true
		}
		if v, err := strconv.ParseUint(e, 10, 16); err == nil && isGREASE(uint16(v)) {
			b.greaseExt = true
		}
	}

	exts := make([]utls.TLSExtension, 0, len(extTokens))
	for _, e := range extTokens {
		ext, err := b.extension(e)
		if err != nil {
			return nil, err
		}
		exts = append(exts, ext)
	}

	spec := &utls.ClientHelloSpec{
		CipherSuites:       suites,
		CompressionMethods: []byte{0},
		Extensions:         exts,
		GetSessionID:       sha256.Sum256,
	}
	return spec, nil
}

// builder 攒着重建一条 spec 需要的上下文。
//
// 存在的理由是**扩展之间不独立**：key_share（51）必须与 supported_groups（10）
// 里的曲线一致，supported_versions（43）决定 key_share 有没有意义。原先那份
// 按 id 查表的写法没有上下文，只能给 key_share 硬编码三条固定曲线 —— 于是一条
// 声称只支持 X25519 的 JA3 串，重建出来会为 P-256 和 P-384 也生成密钥并发出去，
// 那是**任何真实浏览器都不会做的事**（生成三对密钥只为用掉一对）。
type builder struct {
	curves       []utls.CurveID
	pointFormats []byte
	tls13        bool

	// greaseCurve / greaseExt 记录串里**本来就有** GREASE 的那些维度。
	//
	// ★★ 重建只能还原串里有的，**不能凭空添加**。凭空加出来的东西既不是串里
	// 描述的那个客户端，也不是任何真实浏览器 —— 而这一条在 key_share 上会直接
	// 让握手失败，见 keyShare 的注释。
	greaseCurve bool
	greaseExt   bool
}

func (b *builder) extension(token string) (utls.TLSExtension, error) {
	switch token {
	case "0":
		return &utls.SNIExtension{}, nil
	case "5":
		return &utls.StatusRequestExtension{}, nil
	case "10":
		return &utls.SupportedCurvesExtension{Curves: b.curves}, nil
	case "11":
		return &utls.SupportedPointsExtension{SupportedPoints: b.pointFormats}, nil
	case "13":
		return &utls.SignatureAlgorithmsExtension{
			SupportedSignatureAlgorithms: []utls.SignatureScheme{
				utls.ECDSAWithP256AndSHA256,
				utls.PSSWithSHA256,
				utls.PKCS1WithSHA256,
				utls.ECDSAWithP384AndSHA384,
				utls.PSSWithSHA384,
				utls.PKCS1WithSHA384,
				utls.PSSWithSHA512,
				utls.PKCS1WithSHA512,
			},
		}, nil
	case "16":
		return &utls.ALPNExtension{AlpnProtocols: []string{"h2", "http/1.1"}}, nil
	case "17":
		return &utls.StatusRequestV2Extension{}, nil
	case "18":
		return &utls.SCTExtension{}, nil
	case "21":
		return &utls.UtlsPaddingExtension{GetPaddingLen: utls.BoringPaddingStyle}, nil
	case "22":
		return &utls.GenericExtension{Id: 22}, nil // encrypt_then_mac
	case "23":
		return &utls.ExtendedMasterSecretExtension{}, nil
	case "27":
		return &utls.UtlsCompressCertExtension{
			Algorithms: []utls.CertCompressionAlgo{utls.CertCompressionBrotli},
		}, nil
	case "28":
		return &utls.FakeRecordSizeLimitExtension{}, nil
	case "34":
		return &utls.GenericExtension{Id: 34}, nil // delegated_credentials
	case "35":
		return &utls.SessionTicketExtension{}, nil
	case "41":
		// pre_shared_key 必须是 ClientHello 的**最后一个**扩展，且内容是会话票据。
		// 冷启动没有票据可用，重建一个空的会让握手直接失败。
		return nil, ErrExtensionNotExist(token)
	case "43":
		return b.supportedVersions(), nil
	case "44":
		return &utls.CookieExtension{}, nil
	case "45":
		return &utls.PSKKeyExchangeModesExtension{Modes: []uint8{utls.PskModeDHE}}, nil
	case "49":
		return &utls.GenericExtension{Id: 49}, nil // post_handshake_auth
	case "50":
		return &utls.GenericExtension{Id: 50}, nil // signature_algorithms_cert
	case "51":
		return b.keyShare()
	case "13172":
		return &utls.NPNExtension{}, nil
	case "17513":
		// ALPS —— Chrome 从 2021 年起就在发。内容是它愿意在 h2 上协商的
		// application settings，不是空的。
		return &utls.ApplicationSettingsExtension{SupportedProtocols: []string{"h2"}}, nil
	case "65281":
		return &utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateOnceAsClient}, nil
	}

	// GREASE 扩展在串里是一个随机的 0x?A?A 值。
	if v, err := strconv.ParseUint(token, 10, 16); err == nil && isGREASE(uint16(v)) {
		return &utls.UtlsGREASEExtension{}, nil
	}
	return nil, ErrExtensionNotExist(token)
}

// supportedVersions 按串里有没有 43 决定发哪些版本。
//
// ★★ 这里曾经**硬编码成 TLS 1.2/1.1/1.0**，于是每一条 JA3 串重建出来的都是一个
// "只会 TLS 1.2 的浏览器" —— 而 2016 年之后的任何一版 Chrome/Firefox 都协商
// TLS 1.3。伪装的结果是一个明显过时的客户端，比不伪装更容易被挑出来（而且
// TLS 1.0/1.1 早被 RFC 8996 弃用，很多站点已经不接了）。
//
// 不发 1.0/1.1 是有意的：现代浏览器的 supported_versions 里就没有它们。
func (b *builder) supportedVersions() utls.TLSExtension {
	versions := make([]uint16, 0, 3)
	// GREASE 同样只在串里有 GREASE 迹象时才加 —— 见 builder 的注释。
	if b.greaseExt {
		versions = append(versions, utls.GREASE_PLACEHOLDER)
	}
	versions = append(versions, utls.VersionTLS13, utls.VersionTLS12)
	return &utls.SupportedVersionsExtension{Versions: versions}
}

// keyShare 按 supported_groups 的第一条曲线生成 key_share。
//
// 真实浏览器只为**最可能被选中的那一条**曲线预生成密钥（Chrome 是 X25519，
// 外加一个 GREASE 占位）；服务器要别的曲线就发 HelloRetryRequest。为每条曲线都
// 生成一份不是"更保险"，是一个没有任何浏览器会留下的足迹。
//
// ★★ **GREASE 条目只在 supported_groups 里确实有 GREASE 时才加。**
// RFC 8446 §4.2.8 写死了：客户端**不得**为 supported_groups 里没有的 group 提供
// key_share。给一条不含 GREASE 曲线的 JA3 串凭空补一个 GREASE key_share，
// ClientHello 就违规了。
//
// 2026-09-02 实测：Go 的 httptest 服务端**不检查**这一条（本地测试全绿），
// www.ti.com（Akamai）检查，握手直接回 `illegal parameter` —— 一个既不提
// key_share 也不提 supported_groups 的错误。判据是拿掉 GREASE 条目就好。
// 所以这条路上的本地绿灯什么都不证明，守卫得是结构断言
// （TestKeyShareIsSubsetOfSupportedGroups）而不是一次成功的握手。
func (b *builder) keyShare() (utls.TLSExtension, error) {
	var shares []utls.KeyShare
	if b.greaseCurve {
		shares = append(shares, utls.KeyShare{Group: utls.CurveID(utls.GREASE_PLACEHOLDER), Data: []byte{0}})
	}
	for _, c := range b.curves {
		if !isGREASE(uint16(c)) {
			shares = append(shares, utls.KeyShare{Group: c})
			break
		}
	}

	// 曲线段是空的却又列了 key_share —— 这条串自相矛盾。
	//
	// 补一条 X25519 让它"能用"是错的：那正好造出上面说的那个违规 ClientHello
	// （key_share 提了一个 supported_groups 里没有的 group）。报错至少指得到病灶，
	// 而握手失败时那句 `illegal parameter` 指不到任何地方。
	if len(shares) == 0 || (b.greaseCurve && len(shares) == 1) {
		return nil, fmt.Errorf("ja3: 串里列了 key_share(51) 却没有任何椭圆曲线 —— " +
			"客户端不得为 supported_groups 里没有的 group 提供 key_share（RFC 8446 §4.2.8）")
	}
	return &utls.KeyShareExtension{KeyShares: shares}, nil
}

// ── 分段解析 ──────────────────────────────────────────────────────────────

// splitList 切一段短横线分隔的 id，空段返回空切片（而不是 [""]）。
func splitList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return strings.Split(s, "-")
}

func parseCiphers(s string) ([]uint16, error) {
	var out []uint16
	for _, c := range splitList(s) {
		v, err := strconv.ParseUint(c, 10, 16)
		if err != nil {
			return nil, fmt.Errorf("ja3: 密码套件 %q 不是一个 16 位数字: %w", c, err)
		}
		if isGREASE(uint16(v)) {
			out = append(out, utls.GREASE_PLACEHOLDER)
			continue
		}
		out = append(out, uint16(v))
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("ja3: 密码套件段是空的")
	}
	return out, nil
}

// parseCurves 的第二个返回值报告串里有没有 GREASE 曲线 —— key_share 要据此决定
// 加不加 GREASE 条目（RFC 8446 §4.2.8，见 keyShare）。
func parseCurves(s string) ([]utls.CurveID, bool, error) {
	var out []utls.CurveID
	grease := false
	for _, c := range splitList(s) {
		v, err := strconv.ParseUint(c, 10, 16)
		if err != nil {
			return nil, false, fmt.Errorf("ja3: 曲线 %q 不是一个 16 位数字: %w", c, err)
		}
		if isGREASE(uint16(v)) {
			out = append(out, utls.CurveID(utls.GREASE_PLACEHOLDER))
			grease = true
			continue
		}
		out = append(out, utls.CurveID(v))
	}
	return out, grease, nil
}

func parsePointFormats(s string) ([]byte, error) {
	var out []byte
	for _, p := range splitList(s) {
		v, err := strconv.ParseUint(p, 10, 8)
		if err != nil {
			return nil, fmt.Errorf("ja3: 点格式 %q 不是一个 8 位数字: %w", p, err)
		}
		out = append(out, byte(v))
	}
	if len(out) == 0 {
		out = []byte{0} // pointFormatUncompressed，所有浏览器都发
	}
	return out, nil
}
