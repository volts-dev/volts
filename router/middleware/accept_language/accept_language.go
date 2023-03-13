package accept_language

import (
	"sort"
	"strconv"
	"strings"

	"github.com/volts-dev/utils"
	"github.com/volts-dev/volts/router"
)

type (
	TLanguage struct {
		Language string
		Quality  float32
	}

	AcceptLanguages []TLanguage

	TAcceptLanguage struct {
	}
)

func NewAcceptLanguage() router.IMiddleware {
	return &TAcceptLanguage{}
}

func (self *TAcceptLanguage) Handler(hd router.IContext) {
}

func (self *TAcceptLanguage) NearestLang(hd *router.THttpContext) string {
	header := hd.Request().Header().Get("Accept-Language")
	if header != "" {
		acceptLanguageHeaderValues := strings.Split(header, ",")
		acceptLanguages := make(AcceptLanguages, len(acceptLanguageHeaderValues))

		for i, languageRange := range acceptLanguageHeaderValues {
			// Check if a given range is qualified or not
			if qualifiedRange := strings.Split(languageRange, ";q="); len(qualifiedRange) == 2 {
				quality, error := strconv.ParseFloat(qualifiedRange[1], 32)
				if error != nil {
					// When the quality is unparseable, assume it's 1
					acceptLanguages[i] = TLanguage{utils.Trim(qualifiedRange[0]), 1}
				} else {
					acceptLanguages[i] = TLanguage{utils.Trim(qualifiedRange[0]), float32(quality)}
				}
			} else {
				acceptLanguages[i] = TLanguage{utils.Trim(languageRange), 1}
			}
		}
		sort.Slice(acceptLanguages, func(i, j int) bool {
			return acceptLanguages[i].Quality >= acceptLanguages[j].Quality
		})
		return acceptLanguages[0].Language
	} else {
		// If we have no Accept-Language header just map an empty slice
		return ""
	}
}
