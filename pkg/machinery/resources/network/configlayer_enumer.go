// Code generated by "enumer -type=ConfigLayer -linecomment -text"; DO NOT EDIT.

//
package network

import (
	"fmt"
)

const _ConfigLayerName = "defaultcmdlineplatformoperatorconfiguration"

var _ConfigLayerIndex = [...]uint8{0, 7, 14, 22, 30, 43}

func (i ConfigLayer) String() string {
	if i < 0 || i >= ConfigLayer(len(_ConfigLayerIndex)-1) {
		return fmt.Sprintf("ConfigLayer(%d)", i)
	}
	return _ConfigLayerName[_ConfigLayerIndex[i]:_ConfigLayerIndex[i+1]]
}

var _ConfigLayerValues = []ConfigLayer{0, 1, 2, 3, 4}

var _ConfigLayerNameToValueMap = map[string]ConfigLayer{
	_ConfigLayerName[0:7]:   0,
	_ConfigLayerName[7:14]:  1,
	_ConfigLayerName[14:22]: 2,
	_ConfigLayerName[22:30]: 3,
	_ConfigLayerName[30:43]: 4,
}

// ConfigLayerString retrieves an enum value from the enum constants string name.
// Throws an error if the param is not part of the enum.
func ConfigLayerString(s string) (ConfigLayer, error) {
	if val, ok := _ConfigLayerNameToValueMap[s]; ok {
		return val, nil
	}
	return 0, fmt.Errorf("%s does not belong to ConfigLayer values", s)
}

// ConfigLayerValues returns all values of the enum
func ConfigLayerValues() []ConfigLayer {
	return _ConfigLayerValues
}

// IsAConfigLayer returns "true" if the value is listed in the enum definition. "false" otherwise
func (i ConfigLayer) IsAConfigLayer() bool {
	for _, v := range _ConfigLayerValues {
		if i == v {
			return true
		}
	}
	return false
}

// MarshalText implements the encoding.TextMarshaler interface for ConfigLayer
func (i ConfigLayer) MarshalText() ([]byte, error) {
	return []byte(i.String()), nil
}

// UnmarshalText implements the encoding.TextUnmarshaler interface for ConfigLayer
func (i *ConfigLayer) UnmarshalText(text []byte) error {
	var err error
	*i, err = ConfigLayerString(string(text))
	return err
}
