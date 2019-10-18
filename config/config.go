package volts

import (
	"fmt"
	"net/url"
	"path/filepath"

	"github.com/go-ini/ini"
)

const (
	CONFIG_FILE_NAME = "config.ini"
)

var (
	// App settings.
	AppVer       string
	AppName      string
	AppUrl       string
	AppSubUrl    string
	AppPath      string
	AppFilePath  string
	AppDir       string


)

func init() {
	AppFilePath = utils.AppFilePath()
	AppPath = utils.AppPath()
	AppDir = utils.AppDir()
}

func LoadConfig(file_name string) {

}

func SaveConfig() {

}

func Get
