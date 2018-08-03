// Copyright 2018, OpenCensus Authors
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

package signalfx

import (
	"testing"
)

func TestExporterTokenSet(t *testing.T) {
	_, err := NewExporter(Options{Token: "token"})
	if err != nil {
		t.Fatalf("shold not have generater error with token not null. err: %T", err)
	}
}

func TestExporterTokenNotSet(t *testing.T) {
	_, err := NewExporter(Options{})
	if err == nil {
		t.Fatal("shold have generater error with token null")
	} else {
		if err.Error() != "token cannot be empty on options signalfx.Options" {
			t.Fatal("expected empty token error message")
		}
	}
}
