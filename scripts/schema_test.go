package scripts

import (
	"bufio"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/aliyun/terraform-provider-alicloud/alicloud"
	set "github.com/deckarep/golang-set"
	"github.com/hashicorp/terraform-plugin-sdk/helper/schema"
	log "github.com/sirupsen/logrus"
	"github.com/waigani/diffparser"
)

func init() {
	customFormatter := new(log.TextFormatter)
	customFormatter.FullTimestamp = true
	customFormatter.TimestampFormat = "2006-01-02 15:04:05"
	customFormatter.DisableTimestamp = false
	customFormatter.DisableColors = false
	customFormatter.ForceColors = true
	log.SetFormatter(customFormatter)
	log.SetOutput(os.Stdout)
	log.SetLevel(log.DebugLevel)
}

var (
	resourceName = flag.String("resource", "", "the name of the terraform resource to diff")
	fileName     = flag.String("file_name", "", "the file to check diff")
)

type Resource struct {
	Name       string
	Arguments  map[string]interface{}
	Attributes map[string]interface{}
}

func TestConsistencyWithDocument(t *testing.T) {
	flag.Parse()
	if resourceName != nil && len(*resourceName) == 0 {
		log.Warningf("the resource name is empty")
		return
	}
	obj := alicloud.Provider().(*schema.Provider).ResourcesMap[*resourceName].Schema
	objSchema := make(map[string]interface{}, 0)
	objMd, err := parseResource(*resourceName)
	if err != nil {
		log.Error(err)
		t.Fatal()
	}
	mergeMaps(objSchema, objMd.Arguments, objMd.Attributes)

	if consistencyCheck(objSchema, obj) {
		t.Fatal()
	}
}

func TestFieldCompatibilityCheck(t *testing.T) {
	flag.Parse()
	if fileName != nil && len(*fileName) == 0 {
		log.Warningf("the diff file is empty")
		return
	}
	byt, _ := ioutil.ReadFile(*fileName)
	diff, _ := diffparser.Parse(string(byt))
	res := false
	fileRegex := regexp.MustCompile("alicloud/resource[a-zA-Z_]*.go")
	fileTestRegex := regexp.MustCompile("alicloud/resource[a-zA-Z_]*_test.go")
	for _, file := range diff.Files {
		if fileRegex.MatchString(file.NewName) {
			if fileTestRegex.MatchString(file.NewName) {
				continue
			}
			for _, hunk := range file.Hunks {
				if hunk != nil {
					prev := ParseField(hunk.OrigRange, hunk.OrigRange.Length)
					current := ParseField(hunk.NewRange, hunk.NewRange.Length)
					res = CompatibilityRule(prev, current, file.NewName)
				}
			}
		}
	}
	if res {
		t.Fatal("incompatible changes occurred")
	}
}

func CompatibilityRule(prev, current map[string]map[string]interface{}, fileName string) (res bool) {
	for filedName, obj := range prev {
		// Optional -> Required
		_, exist1 := obj["Optional"]
		_, exist2 := current[filedName]["Required"]
		if exist1 && exist2 {
			res = true
			log.Errorf("[Incompatible Change]: there should not change attribute %v to required from optional in the file %v!", fileName, filedName)
		}
		// Type changed
		_, exist1 = obj["Type"]
		_, exist2 = current[filedName]["Type"]
		if exist1 && exist2 {
			res = true
			log.Errorf("[Incompatible Change]: there should not to change the type of attribute %v in the file %v!", fileName, filedName)
		}

		_, exist2 = current[filedName]["ForceNew"]
		if exist2 {
			res = true
			log.Errorf("[Incompatible Change]: there should not to change attribute %v to ForceNew from normal in the file %v!", fileName, filedName)
		}

		// type string: valid values
		validateArrPrev, exist1 := obj["ValidateFuncString"]
		validateArrCurrent, exist2 := current[filedName]["ValidateFuncString"]
		if exist1 && exist2 && len(validateArrPrev.([]string)) > len(validateArrCurrent.([]string)) {
			res = true
			log.Errorf("[Incompatible Change]: attribute %v enum values should not less than before in the file %v!", fileName, filedName)
		}

	}
	return
}

func ParseField(hunk diffparser.DiffRange, length int) map[string]map[string]interface{} {
	schemaRegex := regexp.MustCompile("^\\t*\"([a-zA-Z_]*)\"")
	typeRegex := regexp.MustCompile("^\\t*Type:\\s+schema.([a-zA-Z]*)")
	optionRegex := regexp.MustCompile("^\\t*Optional:\\s+([a-z]*),")
	forceNewRegex := regexp.MustCompile("^\\t*ForceNew:\\s+([a-z]*),")
	requiredRegex := regexp.MustCompile("^\\t*Required:\\s+([a-z]*),")
	validateStringRegex := regexp.MustCompile("^\\t*ValidateFunc: ?validation.StringInSlice\\(\\[\\]string\\{([a-z\\-A-Z_,\"\\s]*)")

	temp := map[string]interface{}{}
	schemaName := ""
	raw := make(map[string]map[string]interface{}, 0)
	for i := 0; i < length; i++ {
		currentLine := hunk.Lines[i]
		content := currentLine.Content
		fieldNameMatched := schemaRegex.FindAllStringSubmatch(content, -1)
		if fieldNameMatched != nil && fieldNameMatched[0] != nil {
			if len(schemaName) != 0 && schemaName != fieldNameMatched[0][1] {
				temp["Name"] = schemaName
				raw[schemaName] = temp
				temp = map[string]interface{}{}
			}
			schemaName = fieldNameMatched[0][1]
		}

		if !schemaRegex.MatchString(currentLine.Content) && currentLine.Mode == diffparser.UNCHANGED {
			continue
		}

		typeMatched := typeRegex.FindAllStringSubmatch(content, -1)
		typeValue := ""
		if typeMatched != nil && typeMatched[0] != nil {
			typeValue = typeMatched[0][1]
			temp["Type"] = typeValue
		}

		optionalMatched := optionRegex.FindAllStringSubmatch(content, -1)
		optionValue := ""
		if optionalMatched != nil && optionalMatched[0] != nil {
			optionValue = optionalMatched[0][1]
			op, _ := strconv.ParseBool(optionValue)
			temp["Optional"] = op
		}

		forceNewMatched := forceNewRegex.FindAllStringSubmatch(content, -1)
		forceNewValue := ""
		if forceNewMatched != nil && forceNewMatched[0] != nil {
			forceNewValue = forceNewMatched[0][1]
			fc, _ := strconv.ParseBool(forceNewValue)
			temp["ForceNew"] = fc
		}

		requiredMatched := requiredRegex.FindAllStringSubmatch(content, -1)
		requiredValue := ""
		if requiredMatched != nil && requiredMatched[0] != nil {
			requiredValue = requiredMatched[0][1]
			rq, _ := strconv.ParseBool(requiredValue)
			temp["Required"] = rq
		}

		validateStringMatched := validateStringRegex.FindAllStringSubmatch(content, -1)
		validateStringValue := ""
		if validateStringMatched != nil && validateStringMatched[0] != nil {
			validateStringValue = validateStringMatched[0][1]
			temp["ValidateFuncString"] = strings.Split(validateStringValue, ",")
		}

	}
	if _, exist := raw[schemaName]; !exist && len(temp) >= 1 {
		temp["Name"] = schemaName
		raw[schemaName] = temp
	}
	return raw
}

func parseResource(resourceName string) (*Resource, error) {
	splitRes := strings.Split(resourceName, "alicloud_")
	if len(splitRes) < 2 {
		log.Errorf("the resource name parsed failed")
		return nil, fmt.Errorf("the resource name parsed failed")
	}
	basePath := "../website/docs/r/"
	filePath := strings.Join([]string{basePath, splitRes[1], ".html.markdown"}, "")

	file, err := os.Open(filePath)
	if err != nil {
		log.Printf("cannot open text file: %s, err: [%v]", filePath, err)
		return nil, err
	}
	defer file.Close()

	argsRegex := regexp.MustCompile("## Argument Reference")
	attribRegex := regexp.MustCompile("## Attributes Reference")
	secondLevelRegex := regexp.MustCompile("^\\#+")
	argumentsFieldRegex := regexp.MustCompile("^\\* `([a-zA-Z_0-9]*)`[ ]*-? ?(\\(.*\\)) ?(.*)")
	attributeFieldRegex := regexp.MustCompile("^\\* `([a-zA-Z_0-9]*)`[ ]*-?(.*)")

	name := filepath.Base(filePath)
	re := regexp.MustCompile("[a-zA-Z_]*")
	resourceName = "alicloud_" + re.FindString(name)
	result := &Resource{Name: resourceName, Arguments: map[string]interface{}{}, Attributes: map[string]interface{}{}}
	log.Infof("the resourceName = %s\n", resourceName)

	scanner := bufio.NewScanner(file)
	argumentFlag := false
	attrFlag := false
	for scanner.Scan() {
		line := scanner.Text()
		if argsRegex.MatchString(line) {
			argumentFlag = true
			continue
		}
		if attribRegex.MatchString(line) {
			argumentFlag = false
			attrFlag = true
			continue
		}
		if argumentFlag {
			if secondLevelRegex.MatchString(line) {
				argumentFlag = false
				continue
			}
			argumentsMatched := argumentsFieldRegex.FindAllStringSubmatch(line, 1)
			for _, argumentMatched := range argumentsMatched {
				Field := parseMatchLine(argumentMatched, true)
				if v, exist := Field["Name"]; exist {
					result.Arguments[v.(string)] = Field
				}
			}
		}

		if attrFlag {
			if secondLevelRegex.MatchString(line) {
				attrFlag = false
				break
			}
			attributesMatched := attributeFieldRegex.FindAllStringSubmatch(line, 1)
			for _, attributeParsed := range attributesMatched {
				Field := parseMatchLine(attributeParsed, false)
				if v, exist := Field["Name"]; exist {
					result.Attributes[v.(string)] = Field
				}
			}
		}
	}
	return result, nil
}

func parseMatchLine(words []string, argumentFlag bool) map[string]interface{} {
	result := make(map[string]interface{}, 0)
	if argumentFlag && len(words) >= 4 {
		result["Name"] = words[1]
		result["Description"] = words[3]
		if strings.Contains(words[2], "Optional") {
			result["Optional"] = true
		}
		if strings.Contains(words[2], "Required") {
			result["Required"] = true
		}
		if strings.Contains(words[2], "ForceNew") {
			result["ForceNew"] = true
		}
		return result
	}
	if !argumentFlag && len(words) >= 3 {
		result["Name"] = words[1]
		result["Description"] = words[2]
		return result
	}
	return nil
}

func consistencyCheck(doc map[string]interface{}, resource map[string]*schema.Schema) (res bool) {
	// the number of the schema field + 1(id) should equal to the number defined in document
	if len(resource)+1 != len(doc) {
		res = true
		record := set.NewSet()
		for field, _ := range doc {
			if field == "id" {
				delete(doc, field)
				continue
			}
			if _, exist := resource[field]; exist {
				delete(doc, field)
				delete(resource, field)
			} else if !exist {
				// the field existed in Document,but not existed in resource
				record.Add(field)
			}
		}
		if len(resource) != 0 {
			for field, _ := range resource {
				// the field existed in resource,but not existed in document
				record.Add(field)
			}
		}
		log.Errorf("there is missing attribute %v description in the document", record)
		return
	}
	for field, docFieldObj := range doc {
		resourceFieldObj := resource[field]
		if _, exist1 := docFieldObj.(map[string]interface{})["Optional"]; exist1 {
			if !resourceFieldObj.Optional {
				res = true
				log.Errorf("attribute %v should be marked as Optional in the in the document description", field)
			}
		}
		if _, exist1 := docFieldObj.(map[string]interface{})["Required"]; exist1 {
			if !resourceFieldObj.Required {
				res = true
				log.Errorf("attribute %v should be marked as Required in the in the document description", field)
			}
		}
		if _, exist1 := docFieldObj.(map[string]interface{})["ForceNew"]; exist1 {
			if !resourceFieldObj.ForceNew {
				res = true
				log.Errorf("attribute %v should be marked as ForceNew in the document description", field)
			}
		}
	}
	return false
}

func mergeMaps(Dst map[string]interface{}, arr ...map[string]interface{}) map[string]interface{} {
	for _, m := range arr {
		for k, v := range m {
			if _, exist := Dst[k]; exist {
				continue
			}
			Dst[k] = v
		}
	}
	return Dst
}
