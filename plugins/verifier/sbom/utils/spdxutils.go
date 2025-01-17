/*
Copyright The Ratify Authors.
Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package utils

import (
	"strings"

	"github.com/spdx/tools-golang/spdx"
)

// Get the packageLicense array from spdxDoc
func GetPackageLicenses(doc spdx.Document) []PackageLicense {
	output := []PackageLicense{}
	for _, p := range doc.Packages {
		output = append(output, PackageLicense{
			Name:    p.PackageName,
			Version: p.PackageVersion,
			License: p.PackageLicenseConcluded,
		})
	}
	return output
}

// returns true if the licenseExpression contains the disallowed license
// this implements a whole word match
func ContainsLicense(spdxLicenseExpression string, disallowed string) bool {
	if len(spdxLicenseExpression) == 0 {
		return false
	}

	// if the licenseExpression is exactly the same as the disallowed license, return true
	if spdxLicenseExpression == disallowed {
		return true
	}

	disallowed1 := disallowed + " "
	disallowed2 := " " + disallowed

	// look for whole word match
	if strings.Contains(spdxLicenseExpression, disallowed1) || strings.Contains(spdxLicenseExpression, disallowed2) {
		return true
	}

	return false
}
