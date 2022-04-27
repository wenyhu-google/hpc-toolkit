/**
 * Copyright 2021 Google LLC
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *      http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package config

import (
	"fmt"
	"log"
	"regexp"
	"strings"

	"hpc-toolkit/pkg/resreader"
	"hpc-toolkit/pkg/sourcereader"
	"hpc-toolkit/pkg/validators"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"
)

const (
	validationErrorMsg = "validation failed due to the issues listed above"
)

// validate is the top-level function for running the validation suite.
func (bc BlueprintConfig) validate() {
	if err := bc.validateVars(); err != nil {
		log.Fatal(err)
	}

	// variables should be validated before running validators
	if err := bc.executeValidators(); err != nil {
		log.Fatal(err)
	}

	if err := bc.validateResources(); err != nil {
		log.Fatal(err)
	}
	if err := bc.validateResourceSettings(); err != nil {
		log.Fatal(err)
	}
}

// performs validation of global variables
func (bc BlueprintConfig) executeValidators() error {
	var errored, warned bool
	implementedValidators := bc.getValidators()

	if bc.Config.ValidationLevel == validationIgnore {
		return nil
	}

	for _, validator := range bc.Config.Validators {
		if f, ok := implementedValidators[validator.Validator]; ok {
			err := f(validator)
			if err != nil {
				var prefix string
				switch bc.Config.ValidationLevel {
				case validationWarning:
					warned = true
					prefix = "warning: "
				default:
					errored = true
					prefix = "error: "
				}
				log.Print(prefix, err)
				log.Println()
			}
		} else {
			errored = true
			log.Printf("%s is not an implemented validator", validator.Validator)
		}
	}

	if warned || errored {
		log.Println("validator failures can indicate a credentials problem.")
		log.Println("troubleshooting info appears at:")
		log.Println()
		log.Println("https://github.com/GoogleCloudPlatform/hpc-toolkit/blob/main/README.md#supplying-cloud-credentials-to-terraform")
		log.Println()
		log.Println("validation can be configured:")
		log.Println("- treat failures as warnings by using the create command")
		log.Println("  with the flag \"--validation-level WARNING\"")
		log.Println("- can be disabled entirely by using the create command")
		log.Println("  with the flag \"--validation-level IGNORE\"")
		log.Println("- a custom set of validators can be configured following")
		log.Println("  instructions at:")
		log.Println()
		log.Println("https://github.com/GoogleCloudPlatform/hpc-toolkit/blob/main/README.md#blueprint-warnings-and-errors")
	}

	if errored {
		return fmt.Errorf(validationErrorMsg)
	}
	return nil
}

// validateVars checks the global variables for viable types
func (bc BlueprintConfig) validateVars() error {
	vars := bc.Config.Vars
	nilErr := "global variable %s was not set"

	// Check for project_id
	if _, ok := vars["project_id"]; !ok {
		log.Println("WARNING: No project_id in global variables")
	}

	// Check type of labels (if they are defined)
	if labels, ok := vars["labels"]; ok {
		if _, ok := labels.(map[string]interface{}); !ok {
			return errors.New("vars.labels must be a map")
		}
	}

	// Check for any nil values
	for key, val := range vars {
		if val == nil {
			return fmt.Errorf(nilErr, key)
		}
	}

	return nil
}

func resource2String(c Resource) string {
	cBytes, _ := yaml.Marshal(&c)
	return string(cBytes)
}

func validateResource(c Resource) error {
	if c.ID == "" {
		return fmt.Errorf("%s\n%s", errorMessages["emptyID"], resource2String(c))
	}
	if c.Source == "" {
		return fmt.Errorf("%s\n%s", errorMessages["emptySource"], resource2String(c))
	}
	if !resreader.IsValidKind(c.Kind) {
		return fmt.Errorf("%s\n%s", errorMessages["wrongKind"], resource2String(c))
	}
	return nil
}

func hasIllegalChars(name string) bool {
	return !regexp.MustCompile(`^[\w\+]+(\s*)[\w-\+\.]+$`).MatchString(name)
}

func validateOutputs(res Resource, resInfo resreader.ResourceInfo) error {

	// Only get the map if needed
	var outputsMap map[string]resreader.VarInfo
	if len(res.Outputs) > 0 {
		outputsMap = resInfo.GetOutputsAsMap()
	}

	// Ensure output exists in the underlying resource
	for _, output := range res.Outputs {
		if _, ok := outputsMap[output]; !ok {
			return fmt.Errorf("%s, module: %s output: %s",
				errorMessages["invalidOutput"], res.ID, output)
		}
	}
	return nil
}

// validateResources ensures parameters set in resources are set correctly.
func (bc BlueprintConfig) validateResources() error {
	for _, grp := range bc.Config.ResourceGroups {
		for _, res := range grp.Resources {
			if err := validateResource(res); err != nil {
				return err
			}
			resInfo := bc.ResourcesInfo[grp.Name][res.Source]
			if err := validateOutputs(res, resInfo); err != nil {
				return err
			}
		}
	}
	return nil
}

type resourceVariables struct {
	Inputs  map[string]bool
	Outputs map[string]bool
}

func validateSettings(
	res Resource,
	info resreader.ResourceInfo) error {

	var cVars = resourceVariables{
		Inputs:  map[string]bool{},
		Outputs: map[string]bool{},
	}

	for _, input := range info.Inputs {
		cVars.Inputs[input.Name] = input.Required
	}
	// Make sure we only define variables that exist
	for k := range res.Settings {
		if _, ok := cVars.Inputs[k]; !ok {
			return fmt.Errorf("%s: Module ID: %s Setting: %s",
				errorMessages["extraSetting"], res.ID, k)
		}
	}
	return nil
}

// validateResourceSettings verifies that no additional settings are provided
// that don't have a counterpart variable in the resource.
func (bc BlueprintConfig) validateResourceSettings() error {
	for _, grp := range bc.Config.ResourceGroups {
		for _, res := range grp.Resources {
			reader := sourcereader.Factory(res.Source)
			info, err := reader.GetResourceInfo(res.Source, res.Kind)
			if err != nil {
				errStr := "failed to get info for module at %s while validating module settings"
				return errors.Wrapf(err, errStr, res.Source)
			}
			if err = validateSettings(res, info); err != nil {
				errStr := "found an issue while validating settings for module at %s"
				return errors.Wrapf(err, errStr, res.Source)
			}
		}
	}
	return nil
}

func (bc *BlueprintConfig) getValidators() map[string]func(validatorConfig) error {
	allValidators := map[string]func(validatorConfig) error{
		testProjectExistsName.String(): bc.testProjectExists,
		testRegionExistsName.String():  bc.testRegionExists,
		testZoneExistsName.String():    bc.testZoneExists,
		testZoneInRegionName.String():  bc.testZoneInRegion,
	}
	return allValidators
}

// check that the keys in inputs and requiredInputs are identical sets of strings
func testInputList(function string, inputs map[string]interface{}, requiredInputs []string) error {
	var errored bool
	for _, requiredInput := range requiredInputs {
		if _, found := inputs[requiredInput]; !found {
			log.Printf("a required input %s was not provided to %s!", requiredInput, function)
			errored = true
		}
	}

	if errored {
		return fmt.Errorf("at least one required input was not provided to %s", function)
	}

	// ensure that no extra inputs were provided by comparing length
	if len(requiredInputs) != len(inputs) {
		errStr := "only %v inputs %s should be provided to %s"
		return fmt.Errorf(errStr, len(requiredInputs), requiredInputs, function)
	}

	return nil
}

func (bc *BlueprintConfig) testProjectExists(validator validatorConfig) error {
	requiredInputs := []string{"project_id"}
	funcName := testProjectExistsName.String()
	funcErrorMsg := fmt.Sprintf("validator %s failed", funcName)

	if validator.Validator != funcName {
		return fmt.Errorf("passed wrong validator to %s implementation", funcName)
	}

	err := testInputList(validator.Validator, validator.Inputs, requiredInputs)
	if err != nil {
		log.Print(funcErrorMsg)
		return err
	}

	projectID, err := bc.getStringValue(validator.Inputs["project_id"])
	if err != nil {
		log.Print(funcErrorMsg)
		return err
	}

	// err is nil or an error
	err = validators.TestProjectExists(projectID)
	if err != nil {
		log.Print(funcErrorMsg)
	}
	return err
}

func (bc *BlueprintConfig) testRegionExists(validator validatorConfig) error {
	requiredInputs := []string{"project_id", "region"}
	funcName := testRegionExistsName.String()
	funcErrorMsg := fmt.Sprintf("validator %s failed", funcName)

	if validator.Validator != funcName {
		return fmt.Errorf("passed wrong validator to %s implementation", funcName)
	}

	err := testInputList(validator.Validator, validator.Inputs, requiredInputs)
	if err != nil {
		return err
	}

	projectID, err := bc.getStringValue(validator.Inputs["project_id"])
	if err != nil {
		log.Print(funcErrorMsg)
		return err
	}
	region, err := bc.getStringValue(validator.Inputs["region"])
	if err != nil {
		log.Print(funcErrorMsg)
		return err
	}

	// err is nil or an error
	err = validators.TestRegionExists(projectID, region)
	if err != nil {
		log.Print(funcErrorMsg)
	}
	return err
}

func (bc *BlueprintConfig) testZoneExists(validator validatorConfig) error {
	requiredInputs := []string{"project_id", "zone"}
	funcName := testZoneExistsName.String()
	funcErrorMsg := fmt.Sprintf("validator %s failed", funcName)

	if validator.Validator != funcName {
		return fmt.Errorf("passed wrong validator to %s implementation", funcName)
	}

	err := testInputList(validator.Validator, validator.Inputs, requiredInputs)
	if err != nil {
		return err
	}

	projectID, err := bc.getStringValue(validator.Inputs["project_id"])
	if err != nil {
		log.Print(funcErrorMsg)
		return err
	}
	zone, err := bc.getStringValue(validator.Inputs["zone"])
	if err != nil {
		log.Print(funcErrorMsg)
		return err
	}

	// err is nil or an error
	err = validators.TestZoneExists(projectID, zone)
	if err != nil {
		log.Print(funcErrorMsg)
	}
	return err
}

func (bc *BlueprintConfig) testZoneInRegion(validator validatorConfig) error {
	requiredInputs := []string{"project_id", "region", "zone"}
	funcName := testZoneInRegionName.String()
	funcErrorMsg := fmt.Sprintf("validator %s failed", funcName)

	if validator.Validator != funcName {
		return fmt.Errorf("passed wrong validator to %s implementation", funcName)
	}

	err := testInputList(validator.Validator, validator.Inputs, requiredInputs)
	if err != nil {
		return err
	}

	projectID, err := bc.getStringValue(validator.Inputs["project_id"])
	if err != nil {
		log.Print(funcErrorMsg)
		return err
	}
	zone, err := bc.getStringValue(validator.Inputs["zone"])
	if err != nil {
		log.Print(funcErrorMsg)
		return err
	}
	region, err := bc.getStringValue(validator.Inputs["region"])
	if err != nil {
		log.Print(funcErrorMsg)
		return err
	}

	// err is nil or an error
	err = validators.TestZoneInRegion(projectID, zone, region)
	if err != nil {
		log.Print(funcErrorMsg)
	}
	return err
}

// return the actual value of a global variable specified by the literal
// variable inputReference in form ((var.project_id))
// if it is a literal global variable defined as a string, return value as string
// in all other cases, return empty string and error
func (bc *BlueprintConfig) getStringValue(inputReference interface{}) (string, error) {
	varRef, ok := inputReference.(string)
	if !ok {
		return "", fmt.Errorf("the value %s cannot be cast to a string", inputReference)
	}

	if IsLiteralVariable(varRef) {
		varSlice := strings.Split(HandleLiteralVariable(varRef), ".")
		varSrc := varSlice[0]
		varName := varSlice[1]

		// because expand has already run, the global variable should have been
		// checked for existence. handle if user has explicitly passed
		// ((var.does_not_exit)) or ((not_a_varsrc.not_a_var))
		if varSrc == "var" {
			if val, ok := bc.Config.Vars[varName]; ok {
				valString, ok := val.(string)
				if ok {
					return valString, nil
				}
				return "", fmt.Errorf("the global variable %s is not a string", inputReference)
			}
		}
	}
	return "", fmt.Errorf("the value %s is not a global variable or was not defined", inputReference)
}
