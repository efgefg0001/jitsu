package singer

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jitsucom/eventnative/logging"
	"github.com/jitsucom/eventnative/schema"
	"github.com/jitsucom/eventnative/typing"
	"io"
)

type SchemaRecord struct {
	Type          string   `json:"type,omitempty"`
	Stream        string   `json:"stream,omitempty"`
	Schema        *Schema  `json:"schema,omitempty"`
	KeyProperties []string `json:"key_properties,omitempty"`
}

type Schema struct {
	Properties map[string]*Property `json:"properties,omitempty"`
}

type Property struct {
	//might be string or []string
	Type       interface{}          `json:"type,omitempty"`
	Format     string               `json:"format,omitempty"`
	Properties map[string]*Property `json:"properties,omitempty"`
}

type OutputRepresentation struct {
	State interface{}
	//[tableName] - {}
	Streams map[string]*StreamRepresentation
}

type StreamRepresentation struct {
	BatchHeader *schema.BatchHeader
	KeyFields   []string
	Objects     []map[string]interface{}
}

func ParseOutput(stdout io.ReadCloser) (*OutputRepresentation, error) {
	outputRepresentation := &OutputRepresentation{
		Streams: map[string]*StreamRepresentation{},
	}

	scanner := bufio.NewScanner(stdout)
	buf := make([]byte, 0, 64*1024)
	scanner.Buffer(buf, 1024*1024)
	for scanner.Scan() {
		lineBytes := scanner.Bytes()

		lineObject := map[string]interface{}{}
		err := json.Unmarshal(lineBytes, &lineObject)
		if err != nil {
			return nil, fmt.Errorf("Error unmarshalling singer output line %s into json: %v", string(lineBytes), err)
		}

		objectType, ok := lineObject["type"]
		if !ok || objectType == "" {
			return nil, fmt.Errorf("Error getting singer object 'type' field from: %s", string(lineBytes))
		}

		switch objectType {
		case "SCHEMA":
			streamRepresentation, err := parseSchema(lineBytes)
			if err != nil {
				return nil, fmt.Errorf("Error parsing singer schema %s: %v", string(lineBytes), err)
			}

			outputRepresentation.Streams[streamRepresentation.BatchHeader.TableName] = streamRepresentation
		case "STATE":
			state, ok := lineObject["value"]
			if !ok {
				return nil, fmt.Errorf("Error parsing singer state line %s: malformed state line 'value' doesn't exist", string(lineBytes))
			}

			outputRepresentation.State = state
		case "RECORD":
			tableName, object, err := parseRecord(lineObject)
			if err != nil {
				return nil, fmt.Errorf("Error parsing singer record line %s: %v", string(lineBytes), err)
			}

			outputRepresentation.Streams[tableName].Objects = append(outputRepresentation.Streams[tableName].Objects, object)
		default:
			return nil, fmt.Errorf("Unknown output line type: %s", objectType)
		}
	}

	err := scanner.Err()
	if err != nil {
		return nil, err
	}

	return outputRepresentation, nil
}

func parseRecord(line map[string]interface{}) (string, map[string]interface{}, error) {
	streamName, ok := line["stream"]
	if !ok {
		return "", nil, errors.New("malformed record line 'stream' doesn't exist")
	}

	record, ok := line["record"]
	if !ok {
		return "", nil, errors.New("malformed record line 'record' doesn't exist")
	}

	object, ok := record.(map[string]interface{})
	if !ok {
		return "", nil, errors.New("malformed record line 'record' must be a json object")
	}

	return fmt.Sprint(streamName), object, nil
}

func parseSchema(schemaBytes []byte) (*StreamRepresentation, error) {
	sr := &SchemaRecord{}
	err := json.Unmarshal(schemaBytes, sr)
	if err != nil {
		return nil, fmt.Errorf("Error unmarshalling schema object: %v", err)
	}

	fields := schema.Fields{}
	parseProperties("", sr.Schema.Properties, fields)

	return &StreamRepresentation{
		BatchHeader: &schema.BatchHeader{TableName: sr.Stream, Fields: fields},
		KeyFields:   sr.KeyProperties,
	}, nil
}

func parseProperties(prefix string, properties map[string]*Property, resultFields schema.Fields) {
	for name, property := range properties {
		var types []string

		switch property.Type.(type) {
		case string:
			types = append(types, property.Type.(string))
		case []interface{}:
			propertyTypesAr := property.Type.([]interface{})
			for _, typeValue := range propertyTypesAr {
				types = append(types, fmt.Sprint(typeValue))
			}
		default:
			logging.Errorf("Unknown singer property [%s] type: %T", name, property.Type)
		}

		for _, t := range types {
			var fieldType typing.DataType
			switch t {
			case "null":
				continue
			case "string":
				if property.Format == "date-time" {
					fieldType = typing.TIMESTAMP
				} else {
					fieldType = typing.STRING
				}
			case "number":
				fieldType = typing.FLOAT64
			case "integer":
				fieldType = typing.INT64
			case "boolean":
				fieldType = typing.BOOL
			case "array":
				fieldType = typing.STRING
			case "object":
				parseProperties(prefix+name+"_", property.Properties, resultFields)
			default:
				logging.Errorf("Unknown type in singer schema: %s", t)
				continue
			}

			resultFields[prefix+name] = schema.NewField(fieldType)
			break
		}
	}
}
