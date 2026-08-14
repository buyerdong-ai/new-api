package ratio_setting

import (
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/types"
)

var modelUserGroupRatioMap = types.NewRWMap[string, map[string]float64]()

type ModelUserGroupRatioSetting struct {
	ModelUserGroupRatio *types.RWMap[string, map[string]float64] `json:"model_user_group_ratio"`
}

var modelUserGroupRatioSetting ModelUserGroupRatioSetting

func init() {
	modelUserGroupRatioSetting = ModelUserGroupRatioSetting{
		ModelUserGroupRatio: modelUserGroupRatioMap,
	}
	config.GlobalConfig.Register("model_user_group_ratio_setting", &modelUserGroupRatioSetting)
}

func GetModelUserGroupRatio(userGroup, modelName string) (float64, bool) {
	modelRatios, ok := modelUserGroupRatioMap.Get(userGroup)
	if !ok {
		return 1, false
	}
	if ratio, exists := modelRatios[modelName]; exists {
		return ratio, true
	}
	if ratio, exists := modelRatios["*"]; exists {
		return ratio, true
	}
	return 1, false
}

func ModelUserGroupRatio2JSONString() string {
	return modelUserGroupRatioMap.MarshalJSONString()
}

func UpdateModelUserGroupRatioByJSONString(jsonStr string) error {
	if err := CheckModelUserGroupRatio(jsonStr); err != nil {
		return err
	}
	return types.LoadFromJsonString(modelUserGroupRatioMap, jsonStr)
}

func CheckModelUserGroupRatio(jsonStr string) error {
	var ratios map[string]map[string]float64
	if err := common.Unmarshal([]byte(jsonStr), &ratios); err != nil {
		return err
	}
	for userGroup, modelRatios := range ratios {
		for modelName, ratio := range modelRatios {
			if ratio < 0 || math.IsNaN(ratio) || math.IsInf(ratio, 0) {
				return fmt.Errorf("model user group ratio must be finite and not less than 0: %s/%s", userGroup, modelName)
			}
		}
	}
	return nil
}