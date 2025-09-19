/*
 * Copyright 2020 bigpigeon. All rights reserved.
 * Use of this source code is governed by a MIT style
 * license that can be found in the LICENSE file.
 *
 */

package main

import (
	"go/ast"
	"go/token"
	"strings"
	"unicode/utf8"
)

type tagFormatter struct {
	Err        error
	f          *ast.File
	fs         *token.FileSet
	needFormat [][]*ast.Field
}

// 全局变量
var globalGormKeyOrder []string
var globalIndexKeys map[string]bool

func (s *tagFormatter) Scan() error {
	ast.Walk(s, s.f)
	return s.Err
}

func (s *tagFormatter) Execute() error {
	for _, fields := range s.needFormat {
		err := fieldsTagFormat(fields)
		if err != nil {
			s.Err = err
			return err
		}
	}
	return s.Err
}

func (s *tagFormatter) recordFields(fwt []*ast.Field) {
	if len(fwt) != 0 {
		s.needFormat = append(s.needFormat, fwt)
	}
}

func getFieldName(node *ast.Field) string {
	if len(node.Names) > 0 {
		return node.Names[0].Name
	}

	return ""
}

func getFieldOrTypeName(node *ast.Field) string {
	if len(node.Names) > 0 {
		return node.Names[0].Name
	}
	//if ident, ok := node.Type.(*ast.Ident); ok {
	//	return ident.Name
	//}
	return ""
}

// getFieldIndex 获取字段在数组中的索引
func getFieldIndex(fields []*ast.Field, target *ast.Field) int {
	for i, field := range fields {
		if field == target {
			return i
		}
	}
	return -1
}

// contains 辅助函数：检查切片是否包含指定元素
func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

type tagFormatterFields struct {
	multiline []*ast.Field
	oneline   []*ast.Field
	anonymous []*ast.Field
	s         *tagFormatter
}

func (fields *tagFormatterFields) reset(f *tagFormatter) {
	f.recordFields(fields.multiline)
	fields.multiline = nil
	f.recordFields(fields.oneline)
	fields.oneline = nil
	f.recordFields(fields.anonymous)
	fields.anonymous = nil
}

func (s *tagFormatter) executor(name string, comments []*ast.CommentGroup, n *ast.StructType) {
	if n.Fields != nil {
		var ffields tagFormatterFields

		if len(n.Fields.List) == 0 {
			return
		}
		preMultiELine := -1
		preEline := -1
		preAnonymousELine := -1
		for _, field := range n.Fields.List {
			fieldName := getFieldOrTypeName(field)
			if field.Tag == nil || fieldFilter(fieldName) == false {
				ffields.reset(s)
				continue
			}

			line := s.fs.Position(field.Pos()).Line
			eline := s.fs.Position(field.End()).Line
			// the one way to distinguish the field with multiline anonymous struct and others
			if len(field.Names) == 0 {
				if line-preAnonymousELine > 1 {
					ffields.reset(s)
				}
				ffields.anonymous = append(ffields.anonymous, field)
				preAnonymousELine = eline
			} else if eline-line > 0 {
				if line-preMultiELine > 1 {
					ffields.reset(s)
				}
				ffields.multiline = append(ffields.multiline, field)
				preMultiELine = eline
			} else {
				if line-preEline > 1 {
					ffields.reset(s)
				}
				ffields.oneline = append(ffields.oneline, field)
				preEline = eline
			}
		}
		ffields.reset(s)
	}
}

func (s *tagFormatter) Visit(node ast.Node) ast.Visitor {
	cmap := ast.NewCommentMap(s.fs, node, s.f.Comments)
	visit := newTopVisit(cmap, s.executor)
	return visit.Visit(node)
}

func fieldsTagFormat(fields []*ast.Field) error {
	// 分析所有字段的标签结构
	fieldTags := make([][]KeyValue, len(fields))
	allTagKeys := make(map[string]bool)

	// 第一遍：解析所有字段的标签，收集所有可能的标签键
	for i, field := range fields {
		_, keyValues, err := ParseTag(field.Tag.Value)
		if err != nil {
			return err
		}
		fieldTags[i] = keyValues
		for _, kv := range keyValues {
			allTagKeys[kv.Key] = true
		}
	}

	// 创建有序的标签键列表（保持原有顺序逻辑）
	var orderedKeys []string
	for _, field := range fields {
		fieldIdx := getFieldIndex(fields, field)
		for _, kv := range fieldTags[fieldIdx] {
			found := false
			for _, key := range orderedKeys {
				if key == kv.Key {
					found = true
					break
				}
			}
			if !found {
				orderedKeys = append(orderedKeys, kv.Key)
			}
		}
	}

	// 检查是否有 gorm 标签需要特殊处理
	hasGorm := allTagKeys["gorm"]
	var gormFields map[int][]string
	var gormMaxLengths []int
	if hasGorm {
		gormFields, gormMaxLengths = alignGormParts(fields)
	}

	// 计算每个位置的最大长度
	tagMaxLengths := make(map[string]int)

	for i, kvs := range fieldTags {
		for _, kv := range kvs {
			var kvLen int
			if kv.Key == "gorm" && hasGorm {
				if parts, exists := gormFields[i]; exists {
					alignedValue := alignGormValue(parts, gormMaxLengths)
					kvLen = utf8.RuneCountInString(kv.Key + `:"` + alignedValue + `"`)
				} else {
					kvLen = utf8.RuneCountInString(kv.String())
				}
			} else {
				kvLen = utf8.RuneCountInString(kv.String())
			}

			if kvLen > tagMaxLengths[kv.Key] {
				tagMaxLengths[kv.Key] = kvLen
			}
		}
	}

	// 应用对齐
	for fieldIdx, field := range fields {
		quote, keyWords, err := ParseTag(field.Tag.Value)
		if err != nil {
			return err
		}

		// 创建当前字段的标签映射
		currentTags := make(map[string]KeyValue)
		for _, kv := range keyWords {
			currentTags[kv.Key] = kv
		}

		var alignedParts []string

		// 按照orderedKeys的顺序处理标签
		for i, key := range orderedKeys {
			if kv, exists := currentTags[key]; exists {
				// 该字段有这个标签
				var kvStr string
				if kv.Key == "gorm" && hasGorm {
					if parts, gormExists := gormFields[fieldIdx]; gormExists {
						alignedValue := alignGormValue(parts, gormMaxLengths)
						kvStr = kv.StringWithGormAlign(alignedValue)
					} else {
						kvStr = kv.String()
					}
				} else {
					kvStr = kv.String()
				}

				// 如果不是最后一个标签，需要对齐
				if i < len(orderedKeys)-1 {
					kvLen := utf8.RuneCountInString(kvStr)
					padding := tagMaxLengths[key] - kvLen
					if padding > 0 {
						kvStr += strings.Repeat(" ", padding)
					}
				}
				alignedParts = append(alignedParts, kvStr)
			} else {
				// 该字段没有这个标签，如果不是最后一个位置，需要补空格
				if i < len(orderedKeys)-1 {
					alignedParts = append(alignedParts, strings.Repeat(" ", tagMaxLengths[key]))
				}
			}
		}

		field.Tag.Value = quote + strings.TrimRight(strings.Join(alignedParts, " "), " ") + quote
		field.Tag.ValuePos = 0
	}
	return nil
}

// alignGormParts 对齐 gorm 标签的各个部分
func alignGormParts(fields []*ast.Field) (map[int][]string, []int) {
	gormFields := make(map[int][]string)
	allGormKeys := make(map[string]bool)

	// 收集所有字段中第一次出现的键顺序
	keyFirstAppearance := make(map[string]int)
	appearanceCounter := 0

	// 第一遍：收集所有 gorm 标签的部分，记录键的首次出现顺序
	for _, field := range fields {
		_, keyValues, err := ParseTag(field.Tag.Value)
		if err != nil {
			continue
		}

		for _, kv := range keyValues {
			if kv.Key == "gorm" && len(kv.GormParts) > 0 {
				for _, part := range kv.GormParts {
					cleanPart := strings.TrimSpace(part)
					if cleanPart == "" {
						continue
					}

					var key string
					if colonIdx := strings.Index(cleanPart, ":"); colonIdx != -1 {
						key = strings.TrimSpace(cleanPart[:colonIdx])
					} else {
						key = cleanPart
					}

					if !allGormKeys[key] {
						allGormKeys[key] = true
						keyFirstAppearance[key] = appearanceCounter
						appearanceCounter++
					}
				}
				break
			}
		}
	}

	// 第二遍：为每个字段记录 gorm 部分
	for i, field := range fields {
		_, keyValues, err := ParseTag(field.Tag.Value)
		if err != nil {
			continue
		}

		for _, kv := range keyValues {
			if kv.Key == "gorm" && len(kv.GormParts) > 0 {
				cleanParts := make([]string, len(kv.GormParts))
				for j, part := range kv.GormParts {
					cleanParts[j] = strings.TrimSpace(part)
				}
				gormFields[i] = cleanParts
				break
			}
		}
	}

	// 定义索引相关的键
	indexRelatedKeys := map[string]bool{
		"PRIMARY KEY": true,
		"index":       true,
		"unique":      true,
		"uniqueIndex": true,
		"UNIQUE":      true,
	}

	// 对键进行分类和排序
	var normalKeys []string
	var indexKeys []string
	var commentKeys []string

	// 分类所有键
	for key := range allGormKeys {
		if key == "comment" {
			commentKeys = append(commentKeys, key)
		} else if indexRelatedKeys[key] {
			indexKeys = append(indexKeys, key)
		} else {
			normalKeys = append(normalKeys, key)
		}
	}

	// 对每个分类内的键按首次出现顺序排序
	sortKeysByAppearance := func(keys []string) {
		for i := 0; i < len(keys)-1; i++ {
			for j := i + 1; j < len(keys); j++ {
				if keyFirstAppearance[keys[i]] > keyFirstAppearance[keys[j]] {
					keys[i], keys[j] = keys[j], keys[i]
				}
			}
		}
	}

	sortKeysByAppearance(normalKeys)
	sortKeysByAppearance(indexKeys)
	sortKeysByAppearance(commentKeys)

	// 组合最终顺序：普通键 -> 索引键 -> 注释键
	var orderedGormKeys []string
	orderedGormKeys = append(orderedGormKeys, normalKeys...)
	orderedGormKeys = append(orderedGormKeys, indexKeys...)
	orderedGormKeys = append(orderedGormKeys, commentKeys...)

	// 计算每个键的最大长度
	keyMaxLengths := make([]int, len(orderedGormKeys))
	for _, parts := range gormFields {
		currentGormTags := make(map[string]string)
		for _, part := range parts {
			if part == "" {
				continue
			}
			if colonIdx := strings.Index(part, ":"); colonIdx != -1 {
				key := strings.TrimSpace(part[:colonIdx])
				currentGormTags[key] = part
			} else {
				currentGormTags[part] = part
			}
		}

		// 按顺序计算长度，包括 comment
		for i, key := range orderedGormKeys {
			if value, exists := currentGormTags[key]; exists {
				if len(value) > keyMaxLengths[i] {
					keyMaxLengths[i] = len(value)
				}
			}
		}
	}

	// 将键顺序和相关信息存储在全局变量中
	globalGormKeyOrder = orderedGormKeys
	globalIndexKeys = indexRelatedKeys

	return gormFields, keyMaxLengths
}

// alignGormValue 对齐 gorm 标签值
func alignGormValue(parts []string, maxLengths []int) string {
	if len(parts) == 0 {
		return ""
	}

	// 创建当前字段的键值映射
	currentGormTags := make(map[string]string)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if colonIdx := strings.Index(part, ":"); colonIdx != -1 {
			key := strings.TrimSpace(part[:colonIdx])
			currentGormTags[key] = part
		} else {
			currentGormTags[part] = part
		}
	}

	var result []string
	for i, key := range globalGormKeyOrder {
		if i >= len(maxLengths) {
			break
		}

		if value, exists := currentGormTags[key]; exists {
			// 该字段有这个键
			if globalIndexKeys[key] {
				// 索引相关键：分号紧跟内容，不需要对齐空格
				result = append(result, value+";")
			} else {
				// 普通键（包括comment）：需要对齐
				if i < len(globalGormKeyOrder)-1 {
					// 不是最后一个键，添加分号和对齐空格
					padding := maxLengths[i] - len(value)
					if padding > 0 {
						result = append(result, value+";"+strings.Repeat(" ", padding))
					} else {
						result = append(result, value+";")
					}
				} else {
					// 最后一个键，不添加分号
					result = append(result, value)
				}
			}
		} else {
			// 该字段没有这个键，需要补空格来保持对齐
			if globalIndexKeys[key] {
				// 索引相关键：不存在时补空格，但不加分号
				// 这里要补足够的空格来保持后续字段的对齐
				result = append(result, strings.Repeat(" ", maxLengths[i]+1)) // +1 是为了分号的位置
			} else {
				// 普通键（包括comment）：补空格对齐
				if i < len(globalGormKeyOrder)-1 {
					result = append(result, strings.Repeat(" ", maxLengths[i]+1)) // +1 是为了分号
				} else {
					// 最后一个键位置，不加分号，但仍需要补空格以保持结构
					result = append(result, strings.Repeat(" ", maxLengths[i]))
				}
			}
		}
	}

	// 去除尾部空格
	finalResult := strings.TrimRight(strings.Join(result, " "), " ")

	return finalResult
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func newTagFmt(f *ast.File, fs *token.FileSet) *tagFormatter {
	s := &tagFormatter{fs: fs, f: f}
	return s
}
