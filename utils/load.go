package utils

import (
	"net/url"
	"path/filepath"
	"strings"

	nuclei_parse "github.com/WAY29/pocV/pkg/nuclei/parse"
	nuclei_structs "github.com/WAY29/pocV/pkg/nuclei/structs"
	xray_parse "github.com/WAY29/pocV/pkg/xray/parse"
	xray_structs "github.com/WAY29/pocV/pkg/xray/structs"
)

// 读取目标
func LoadTargets(target *[]string, targetFiles *[]string) []string {
	targetsSlice := make([]string, 0)
	if len(*target) != 0 {
		targetsSlice = append(targetsSlice, *target...)
	}

	for _, targetFile := range *targetFiles {
		if Exists(targetFile) && IsFile(targetFile) {
			DebugF("Load target file: %v", targetFile)

			lineSlice, err := ReadFileAsLine(targetFile)
			if err != nil {
				CliError("Read target file error: "+err.Error(), 2)
			}
			targetsSlice = append(targetsSlice, lineSlice...)
		} else {
			WarningF("Target file not found: %v", targetFile)
		}
	}

	// 检查目标是否是合法的url
	for _, target := range targetsSlice {
		_, err := url.ParseRequestURI(target)
		if err != nil {
			CliError("Target invalid: "+target, 3)
		}
	}

	return targetsSlice
}

// 读取pocs
func LoadPocs(pocs *[]string, pocPaths *[]string) ([]xray_structs.Poc, []nuclei_structs.Poc) {
	xrayPocsSlice := make([]xray_structs.Poc, 0)
	nucleiPocsSlice := make([]nuclei_structs.Poc, 0)

	// 加载poc函数
	LoadPoc := func(pocFile string) {
		if Exists(pocFile) && IsFile(pocFile) {
			DebugF("Load poc file: %v", pocFile)
			// 判断前三个字符
			data, err := ReadFileN(pocFile, 3)

			if err != nil {
				CliError("Read poc file error: "+pocFile, 4)
			}

			// 如果是id: 则为nuclei
			if string(data) == "id:" {
				nucleiPoc, err := nuclei_parse.ParseYaml(pocFile)
				if nucleiPoc.ID == "" || err != nil {
					CliError("Parse yaml error: "+pocFile, 5)
				}

				nucleiPocsSlice = append(nucleiPocsSlice, *nucleiPoc)
			} else {
				xrayPoc, err := xray_parse.ParseYaml(pocFile)
				if xrayPoc.Name == "" || err != nil {
					CliError("Parse yaml error: "+pocFile, 5)
				}
				xrayPocsSlice = append(xrayPocsSlice, *xrayPoc)
			}

		} else {
			WarningF("Poc file not found: %v", pocFile)
		}
	}

	for _, pocFile := range *pocs {
		LoadPoc(pocFile)
	}
	for _, pocPath := range *pocPaths {
		DebugF("Load from poc path: %v", pocPath)

		pocFiles, err := filepath.Glob(pocPath)
		if err != nil {
			CliError("Path glob match error: "+err.Error(), 6)
		}
		for _, pocFile := range pocFiles {
			// 只解析yml或yaml文件
			if strings.HasSuffix(pocFile, ".yml") || strings.HasSuffix(pocFile, ".yaml") {
				LoadPoc(pocFile)
			}
		}

	}

	return xrayPocsSlice, nucleiPocsSlice
}