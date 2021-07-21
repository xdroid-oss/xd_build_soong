// Copyright 2021 Google Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cc

// This file contains the common code for compiling C/C++ and Rust fuzzers for Android.

import (
	"sort"
	"strings"

	"android/soong/android"
)

type Lang string

const (
	Cc   Lang = ""
	Rust Lang = "rust"
)

type FuzzModule struct {
	android.ModuleBase
	android.DefaultableModuleBase
	android.ApexModuleBase
}

type FuzzPackager struct {
	Packages    android.Paths
	FuzzTargets map[string]bool
}

type FileToZip struct {
	SourceFilePath        android.Path
	DestinationPathPrefix string
}

type ArchOs struct {
	HostOrTarget string
	Arch         string
	Dir          string
}

type FuzzPackagedModule struct {
	FuzzProperties        FuzzProperties
	Dictionary            android.Path
	Corpus                android.Paths
	CorpusIntermediateDir android.Path
	Config                android.Path
	Data                  android.Paths
	DataIntermediateDir   android.Path
}

func IsValid(fuzzModule FuzzModule) bool {
	// Discard ramdisk + vendor_ramdisk + recovery modules, they're duplicates of
	// fuzz targets we're going to package anyway.
	if !fuzzModule.Enabled() || fuzzModule.InRamdisk() || fuzzModule.InVendorRamdisk() || fuzzModule.InRecovery() {
		return false
	}

	// Discard modules that are in an unavailable namespace.
	if !fuzzModule.ExportedToMake() {
		return false
	}

	return true
}

func (s *FuzzPackager) PackageArtifacts(ctx android.SingletonContext, module android.Module, fuzzModule FuzzPackagedModule, archDir android.OutputPath, builder *android.RuleBuilder) []FileToZip {
	// Package the corpora into a zipfile.
	var files []FileToZip
	if fuzzModule.Corpus != nil {
		corpusZip := archDir.Join(ctx, module.Name()+"_seed_corpus.zip")
		command := builder.Command().BuiltTool("soong_zip").
			Flag("-j").
			FlagWithOutput("-o ", corpusZip)
		rspFile := corpusZip.ReplaceExtension(ctx, "rsp")
		command.FlagWithRspFileInputList("-r ", rspFile, fuzzModule.Corpus)
		files = append(files, FileToZip{corpusZip, ""})
	}

	// Package the data into a zipfile.
	if fuzzModule.Data != nil {
		dataZip := archDir.Join(ctx, module.Name()+"_data.zip")
		command := builder.Command().BuiltTool("soong_zip").
			FlagWithOutput("-o ", dataZip)
		for _, f := range fuzzModule.Data {
			intermediateDir := strings.TrimSuffix(f.String(), f.Rel())
			command.FlagWithArg("-C ", intermediateDir)
			command.FlagWithInput("-f ", f)
		}
		files = append(files, FileToZip{dataZip, ""})
	}

	// The dictionary.
	if fuzzModule.Dictionary != nil {
		files = append(files, FileToZip{fuzzModule.Dictionary, ""})
	}

	// Additional fuzz config.
	if fuzzModule.Config != nil {
		files = append(files, FileToZip{fuzzModule.Config, ""})
	}

	return files
}

func (s *FuzzPackager) BuildZipFile(ctx android.SingletonContext, module android.Module, fuzzModule FuzzPackagedModule, files []FileToZip, builder *android.RuleBuilder, archDir android.OutputPath, archString string, hostOrTargetString string, archOs ArchOs, archDirs map[ArchOs][]FileToZip) ([]FileToZip, bool) {
	fuzzZip := archDir.Join(ctx, module.Name()+".zip")

	command := builder.Command().BuiltTool("soong_zip").
		Flag("-j").
		FlagWithOutput("-o ", fuzzZip)

	for _, file := range files {
		if file.DestinationPathPrefix != "" {
			command.FlagWithArg("-P ", file.DestinationPathPrefix)
		} else {
			command.Flag("-P ''")
		}
		command.FlagWithInput("-f ", file.SourceFilePath)
	}

	builder.Build("create-"+fuzzZip.String(),
		"Package "+module.Name()+" for "+archString+"-"+hostOrTargetString)

	// Don't add modules to 'make haiku-rust' that are set to not be
	// exported to the fuzzing infrastructure.
	if config := fuzzModule.FuzzProperties.Fuzz_config; config != nil {
		if strings.Contains(hostOrTargetString, "host") && !BoolDefault(config.Fuzz_on_haiku_host, true) {
			return archDirs[archOs], false
		} else if !BoolDefault(config.Fuzz_on_haiku_device, true) {
			return archDirs[archOs], false
		}
	}

	s.FuzzTargets[module.Name()] = true
	archDirs[archOs] = append(archDirs[archOs], FileToZip{fuzzZip, ""})

	return archDirs[archOs], true
}

func (s *FuzzPackager) CreateFuzzPackage(ctx android.SingletonContext, archDirs map[ArchOs][]FileToZip, lang Lang) {
	var archOsList []ArchOs
	for archOs := range archDirs {
		archOsList = append(archOsList, archOs)
	}
	sort.Slice(archOsList, func(i, j int) bool { return archOsList[i].Dir < archOsList[j].Dir })

	for _, archOs := range archOsList {
		filesToZip := archDirs[archOs]
		arch := archOs.Arch
		hostOrTarget := archOs.HostOrTarget
		builder := android.NewRuleBuilder(pctx, ctx)
		zipFileName := "fuzz-" + hostOrTarget + "-" + arch + ".zip"
		if lang == Rust {
			zipFileName = "fuzz-rust-" + hostOrTarget + "-" + arch + ".zip"
		}
		outputFile := android.PathForOutput(ctx, zipFileName)

		s.Packages = append(s.Packages, outputFile)

		command := builder.Command().BuiltTool("soong_zip").
			Flag("-j").
			FlagWithOutput("-o ", outputFile).
			Flag("-L 0") // No need to try and re-compress the zipfiles.

		for _, fileToZip := range filesToZip {

			if fileToZip.DestinationPathPrefix != "" {
				command.FlagWithArg("-P ", fileToZip.DestinationPathPrefix)
			} else {
				command.Flag("-P ''")
			}
			command.FlagWithInput("-f ", fileToZip.SourceFilePath)

		}
		builder.Build("create-fuzz-package-"+arch+"-"+hostOrTarget,
			"Create fuzz target packages for "+arch+"-"+hostOrTarget)
	}
}

func (s *FuzzPackager) PreallocateSlice(ctx android.MakeVarsContext, targets string) {
	fuzzTargets := make([]string, 0, len(s.FuzzTargets))
	for target, _ := range s.FuzzTargets {
		fuzzTargets = append(fuzzTargets, target)
	}
	sort.Strings(fuzzTargets)
	ctx.Strict(targets, strings.Join(fuzzTargets, " "))
}
