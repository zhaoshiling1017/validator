/**
 * Package validator
 *
 * MISC:
 * - anonymous structs - they don't have names so expect the Struct name within StructErrors to be blank
 *
 */

package validator

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	utf8HexComma            = "0x2C"
	utf8Pipe                = "0x7C"
	tagSeparator            = ","
	orSeparator             = "|"
	tagKeySeparator         = "="
	structOnlyTag           = "structonly"
	omitempty               = "omitempty"
	skipValidationTag       = "-"
	diveTag                 = "dive"
	existsTag               = "exists"
	fieldErrMsg             = "Key: '%s' Error:Field validation for '%s' failed on the '%s' tag"
	arrayIndexFieldName     = "%s" + leftBracket + "%d" + rightBracket
	mapIndexFieldName       = "%s" + leftBracket + "%v" + rightBracket
	invalidValidation       = "Invalid validation tag on field %s"
	undefinedValidation     = "Undefined validation function on field %s"
	validatorNotInitialized = "Validator instance not initialized"
	fieldNameRequired       = "Field Name Required"
	tagRequired             = "Tag Required"
)

var (
	timeType       = reflect.TypeOf(time.Time{})
	timePtrType    = reflect.TypeOf(&time.Time{})
	emptyStructPtr = new(struct{})
)

type cachedTag struct {
	isOmitEmpty bool
	diveTag     string
	tags        []*tagVals
}

type tagVals struct {
	tagVals [][]string
	isOrVal bool
	isAlias bool
	tag     string
}

type tagCacheMap struct {
	lock sync.RWMutex
	m    map[string]*cachedTag
}

func (s *tagCacheMap) Get(key string) (*cachedTag, bool) {
	s.lock.RLock()
	defer s.lock.RUnlock()
	value, ok := s.m[key]
	return value, ok
}

func (s *tagCacheMap) Set(key string, value *cachedTag) {
	s.lock.Lock()
	defer s.lock.Unlock()
	s.m[key] = value
}

// StructLevel contains all of the information and helper methods
// for reporting errors during struct level validation
type StructLevel struct {
	TopStruct     reflect.Value
	CurrentStruct reflect.Value
	errPrefix     string
	errs          ValidationErrors
	v             *Validate
}

// ReportValidationErrors accepts the key relative to the top level struct and validatin errors.
// Example: had a triple nested struct User, ContactInfo, Country and ran errs := validate.Struct(country)
// from within a User struct level validation would call this method like so:
// ReportValidationErrors("ContactInfo.", errs)
func (sl *StructLevel) ReportValidationErrors(relativeKey string, errs ValidationErrors) {
	for _, e := range errs {
		sl.errs[sl.errPrefix+relativeKey+e.Field] = e
	}
}

// ReportError reports an error just by passing the field and tag information
// NOTE: tag can be an existing validation tag or just something you make up
// and precess on the flip side it's up to you.
func (sl *StructLevel) ReportError(field reflect.Value, fieldName string, customName string, tag string) {

	field, kind := sl.v.ExtractType(field)

	if len(fieldName) == 0 {
		panic(fieldNameRequired)
	}

	if len(customName) == 0 {
		customName = fieldName
	}

	if len(tag) == 0 {
		panic(tagRequired)
	}

	switch kind {
	case reflect.Invalid:
		sl.errs[sl.errPrefix+fieldName] = &FieldError{
			Name:      customName,
			Field:     fieldName,
			Tag:       tag,
			ActualTag: tag,
			Param:     blank,
			Kind:      kind,
		}
	default:
		sl.errs[sl.errPrefix+fieldName] = &FieldError{
			Name:      customName,
			Field:     fieldName,
			Tag:       tag,
			ActualTag: tag,
			Param:     blank,
			Value:     field.Interface(),
			Kind:      kind,
			Type:      field.Type(),
		}
	}
}

// Validate contains the validator settings passed in using the Config struct
type Validate struct {
	tagName             string
	fieldNameTag        string
	validationFuncs     map[string]Func
	structLevelFuncs    map[reflect.Type]StructLevelFunc
	customTypeFuncs     map[reflect.Type]CustomTypeFunc
	aliasValidators     map[string]string
	hasCustomFuncs      bool
	hasAliasValidators  bool
	hasStructLevelFuncs bool
	tagsCache           *tagCacheMap
	errsPool            *sync.Pool
}

func (v *Validate) initCheck() {
	if v == nil {
		panic(validatorNotInitialized)
	}
}

// Config contains the options that a Validator instance will use.
// It is passed to the New() function
type Config struct {
	TagName      string
	FieldNameTag string
}

// CustomTypeFunc allows for overriding or adding custom field type handler functions
// field = field value of the type to return a value to be validated
// example Valuer from sql drive see https://golang.org/src/database/sql/driver/types.go?s=1210:1293#L29
type CustomTypeFunc func(field reflect.Value) interface{}

// Func accepts all values needed for file and cross field validation
// v             = validator instance, needed but some built in functions for it's custom types
// topStruct     = top level struct when validating by struct otherwise nil
// currentStruct = current level struct when validating by struct otherwise optional comparison value
// field         = field value for validation
// param         = parameter used in validation i.e. gt=0 param would be 0
type Func func(v *Validate, topStruct reflect.Value, currentStruct reflect.Value, field reflect.Value, fieldtype reflect.Type, fieldKind reflect.Kind, param string) bool

// StructLevelFunc accepts all values needed for struct level validation
type StructLevelFunc func(v *Validate, structLevel *StructLevel)

// ValidationErrors is a type of map[string]*FieldError
// it exists to allow for multiple errors to be passed from this library
// and yet still subscribe to the error interface
type ValidationErrors map[string]*FieldError

// Error is intended for use in development + debugging and not intended to be a production error message.
// It allows ValidationErrors to subscribe to the Error interface.
// All information to create an error message specific to your application is contained within
// the FieldError found within the ValidationErrors map
func (ve ValidationErrors) Error() string {

	buff := bytes.NewBufferString(blank)

	for key, err := range ve {
		buff.WriteString(fmt.Sprintf(fieldErrMsg, key, err.Field, err.Tag))
		buff.WriteString("\n")
	}

	return strings.TrimSpace(buff.String())
}

// FieldError contains a single field's validation error along
// with other properties that may be needed for error message creation
type FieldError struct {
	Field     string
	Name      string
	Tag       string
	ActualTag string
	Kind      reflect.Kind
	Type      reflect.Type
	Param     string
	Value     interface{}
}

// New creates a new Validate instance for use.
func New(config *Config) *Validate {

	v := &Validate{
		tagName:      config.TagName,
		fieldNameTag: config.FieldNameTag,
		tagsCache:    &tagCacheMap{m: map[string]*cachedTag{}},
		errsPool: &sync.Pool{New: func() interface{} {
			return ValidationErrors{}
		}}}

	if len(v.aliasValidators) == 0 {
		// must copy alias validators for separate validations to be used in each validator instance
		v.aliasValidators = map[string]string{}
		for k, val := range bakedInAliasValidators {
			v.RegisterAliasValidation(k, val)
		}
	}

	if len(v.validationFuncs) == 0 {
		// must copy validators for separate validations to be used in each instance
		v.validationFuncs = map[string]Func{}
		for k, val := range bakedInValidators {
			v.RegisterValidation(k, val)
		}
	}

	return v
}

// RegisterStructValidation registers a StructLevelFunc against a number of types
// NOTE: this method is not thread-safe it is intended that these all be registered prior to any validation
func (v *Validate) RegisterStructValidation(fn StructLevelFunc, types ...interface{}) {
	v.initCheck()

	if v.structLevelFuncs == nil {
		v.structLevelFuncs = map[reflect.Type]StructLevelFunc{}
	}

	for _, t := range types {
		v.structLevelFuncs[reflect.TypeOf(t)] = fn
	}

	v.hasStructLevelFuncs = true
}

// RegisterValidation adds a validation Func to a Validate's map of validators denoted by the key
// NOTE: if the key already exists, the previous validation function will be replaced.
// NOTE: this method is not thread-safe it is intended that these all be registered prior to any validation
func (v *Validate) RegisterValidation(key string, fn Func) error {
	v.initCheck()

	if len(key) == 0 {
		return errors.New("Function Key cannot be empty")
	}

	if fn == nil {
		return errors.New("Function cannot be empty")
	}

	_, ok := restrictedTags[key]

	if ok || strings.ContainsAny(key, restrictedTagChars) {
		panic(fmt.Sprintf(restrictedTagErr, key))
	}

	v.validationFuncs[key] = fn

	return nil
}

// RegisterCustomTypeFunc registers a CustomTypeFunc against a number of types
// NOTE: this method is not thread-safe it is intended that these all be registered prior to any validation
func (v *Validate) RegisterCustomTypeFunc(fn CustomTypeFunc, types ...interface{}) {
	v.initCheck()

	if v.customTypeFuncs == nil {
		v.customTypeFuncs = map[reflect.Type]CustomTypeFunc{}
	}

	for _, t := range types {
		v.customTypeFuncs[reflect.TypeOf(t)] = fn
	}

	v.hasCustomFuncs = true
}

// RegisterAliasValidation registers a mapping of a single validationstag that
// defines a common or complex set of validation(s) to simplify adding validation
// to structs. NOTE: when returning an error the tag returned in FieldError will be
// the alias tag unless the dive tag is part of the alias; everything after the
// dive tag is not reported as the alias tag. Also the ActualTag in the before case
// will be the actual tag within the alias that failed.
// NOTE: this method is not thread-safe it is intended that these all be registered prior to any validation
func (v *Validate) RegisterAliasValidation(alias, tags string) {
	v.initCheck()

	_, ok := restrictedTags[alias]

	if ok || strings.ContainsAny(alias, restrictedTagChars) {
		panic(fmt.Sprintf(restrictedAliasErr, alias))
	}

	v.aliasValidators[alias] = tags
	v.hasAliasValidators = true
}

// Field validates a single field using tag style validation and returns nil or ValidationErrors as type error.
// You will need to assert the error if it's not nil i.e. err.(validator.ValidationErrors) to access the map of errors.
// NOTE: it returns ValidationErrors instead of a single FieldError because this can also
// validate Array, Slice and maps fields which may contain more than one error
func (v *Validate) Field(field interface{}, tag string) error {
	v.initCheck()

	errs := v.errsPool.Get().(ValidationErrors)
	fieldVal := reflect.ValueOf(field)

	v.traverseField(fieldVal, fieldVal, fieldVal, blank, errs, false, tag, blank, blank, false, false, nil)

	if len(errs) == 0 {
		v.errsPool.Put(errs)
		return nil
	}

	return errs
}

// FieldWithValue validates a single field, against another fields value using tag style validation and returns nil or ValidationErrors.
// You will need to assert the error if it's not nil i.e. err.(validator.ValidationErrors) to access the map of errors.
// NOTE: it returns ValidationErrors instead of a single FieldError because this can also
// validate Array, Slice and maps fields which may contain more than one error
func (v *Validate) FieldWithValue(val interface{}, field interface{}, tag string) error {
	v.initCheck()

	errs := v.errsPool.Get().(ValidationErrors)
	topVal := reflect.ValueOf(val)

	v.traverseField(topVal, topVal, reflect.ValueOf(field), blank, errs, false, tag, blank, blank, false, false, nil)

	if len(errs) == 0 {
		v.errsPool.Put(errs)
		return nil
	}

	return errs
}

// StructPartial validates the fields passed in only, ignoring all others.
// Fields may be provided in a namespaced fashion relative to the  struct provided
// i.e. NestedStruct.Field or NestedArrayField[0].Struct.Name and returns nil or ValidationErrors as error
// You will need to assert the error if it's not nil i.e. err.(validator.ValidationErrors) to access the map of errors.
func (v *Validate) StructPartial(current interface{}, fields ...string) error {
	v.initCheck()

	sv, _ := v.ExtractType(reflect.ValueOf(current))
	name := sv.Type().Name()
	m := map[string]*struct{}{}

	if fields != nil {
		for _, k := range fields {

			flds := strings.Split(k, namespaceSeparator)
			if len(flds) > 0 {

				key := name + namespaceSeparator
				for _, s := range flds {

					idx := strings.Index(s, leftBracket)

					if idx != -1 {
						for idx != -1 {
							key += s[:idx]
							m[key] = emptyStructPtr

							idx2 := strings.Index(s, rightBracket)
							idx2++
							key += s[idx:idx2]
							m[key] = emptyStructPtr
							s = s[idx2:]
							idx = strings.Index(s, leftBracket)
						}
					} else {

						key += s
						m[key] = emptyStructPtr
					}

					key += namespaceSeparator
				}
			}
		}
	}

	errs := v.errsPool.Get().(ValidationErrors)

	v.tranverseStruct(sv, sv, sv, blank, errs, true, len(m) != 0, false, m)

	if len(errs) == 0 {
		v.errsPool.Put(errs)
		return nil
	}

	return errs
}

// StructExcept validates all fields except the ones passed in.
// Fields may be provided in a namespaced fashion relative to the  struct provided
// i.e. NestedStruct.Field or NestedArrayField[0].Struct.Name and returns nil or ValidationErrors as error
// You will need to assert the error if it's not nil i.e. err.(validator.ValidationErrors) to access the map of errors.
func (v *Validate) StructExcept(current interface{}, fields ...string) error {
	v.initCheck()

	sv, _ := v.ExtractType(reflect.ValueOf(current))
	name := sv.Type().Name()
	m := map[string]*struct{}{}

	for _, key := range fields {
		m[name+"."+key] = emptyStructPtr
	}

	errs := v.errsPool.Get().(ValidationErrors)

	v.tranverseStruct(sv, sv, sv, blank, errs, true, len(m) != 0, true, m)

	if len(errs) == 0 {
		v.errsPool.Put(errs)
		return nil
	}

	return errs
}

// Struct validates a structs exposed fields, and automatically validates nested structs, unless otherwise specified.
// it returns nil or ValidationErrors as error.
// You will need to assert the error if it's not nil i.e. err.(validator.ValidationErrors) to access the map of errors.
func (v *Validate) Struct(current interface{}) error {
	v.initCheck()

	errs := v.errsPool.Get().(ValidationErrors)
	sv := reflect.ValueOf(current)

	v.tranverseStruct(sv, sv, sv, blank, errs, true, false, false, nil)

	if len(errs) == 0 {
		v.errsPool.Put(errs)
		return nil
	}

	return errs
}

// tranverseStruct traverses a structs fields and then passes them to be validated by traverseField
func (v *Validate) tranverseStruct(topStruct reflect.Value, currentStruct reflect.Value, current reflect.Value, errPrefix string, errs ValidationErrors, useStructName bool, partial bool, exclude bool, includeExclude map[string]*struct{}) {

	if current.Kind() == reflect.Ptr && !current.IsNil() {
		current = current.Elem()
	}

	if current.Kind() != reflect.Struct && current.Kind() != reflect.Interface {
		panic("value passed for validation is not a struct")
	}

	var ok bool
	typ := current.Type()

	if useStructName {
		errPrefix += typ.Name() + "."
	}

	numFields := current.NumField()

	var fld reflect.StructField
	var customName string

	for i := 0; i < numFields; i++ {
		fld = typ.Field(i)

		if !unicode.IsUpper(rune(fld.Name[0])) {
			continue
		}

		if partial {

			_, ok = includeExclude[errPrefix+fld.Name]

			if (ok && exclude) || (!ok && !exclude) {
				continue
			}
		}

		customName = fld.Name
		if v.fieldNameTag != "" {

			name := strings.SplitN(fld.Tag.Get(v.fieldNameTag), ",", 2)[0]

			// dash check is for json "-" means don't output in json
			if name != "" && name != "-" {
				customName = name
			}
		}

		v.traverseField(topStruct, currentStruct, current.Field(i), errPrefix, errs, true, fld.Tag.Get(v.tagName), fld.Name, customName, partial, exclude, includeExclude)
	}

	// check if any struct level validations, after all field validations already checked.
	if v.hasStructLevelFuncs {
		if fn, ok := v.structLevelFuncs[current.Type()]; ok {
			fn(v, &StructLevel{v: v, TopStruct: topStruct, CurrentStruct: current, errPrefix: errPrefix, errs: errs})
		}
	}
}

// traverseField validates any field, be it a struct or single field, ensures it's validity and passes it along to be validated via it's tag options
func (v *Validate) traverseField(topStruct reflect.Value, currentStruct reflect.Value, current reflect.Value, errPrefix string, errs ValidationErrors, isStructField bool, tag, name, customName string, partial bool, exclude bool, includeExclude map[string]*struct{}) {

	if tag == skipValidationTag {
		return
	}

	cTag, isCached := v.tagsCache.Get(tag)

	if !isCached {
		cTag = v.parseTags(tag, name)
		v.tagsCache.Set(tag, cTag)
	}

	current, kind := v.ExtractType(current)
	var typ reflect.Type

	switch kind {
	case reflect.Ptr, reflect.Interface, reflect.Invalid:
		if cTag.isOmitEmpty {
			return
		}

		if len(tag) > 0 {

			if kind == reflect.Invalid {
				errs[errPrefix+name] = &FieldError{
					Name:      customName,
					Field:     name,
					Tag:       cTag.tags[0].tag,
					ActualTag: cTag.tags[0].tagVals[0][0],
					Param:     cTag.tags[0].tagVals[0][1],
					Kind:      kind,
				}
				return
			}

			errs[errPrefix+name] = &FieldError{
				Name:      customName,
				Field:     name,
				Tag:       cTag.tags[0].tag,
				ActualTag: cTag.tags[0].tagVals[0][0],
				Param:     cTag.tags[0].tagVals[0][1],
				Value:     current.Interface(),
				Kind:      kind,
				Type:      current.Type(),
			}

			return
		}

		// if we get here tag length is zero and we can leave
		if kind == reflect.Invalid {
			return
		}

	case reflect.Struct:
		typ = current.Type()

		if typ != timeType {

			// required passed validation above so stop here
			// if only validating the structs existance.
			if strings.Contains(tag, structOnlyTag) {
				return
			}

			v.tranverseStruct(topStruct, current, current, errPrefix+name+".", errs, false, partial, exclude, includeExclude)
			return
		}
	}

	if len(tag) == 0 {
		return
	}

	typ = current.Type()

	var dive bool
	var diveSubTag string

	for _, valTag := range cTag.tags {

		if valTag.tagVals[0][0] == existsTag {
			continue
		}

		if valTag.tagVals[0][0] == diveTag {
			dive = true
			diveSubTag = strings.TrimLeft(strings.SplitN(cTag.diveTag, diveTag, 2)[1], ",")
			break
		}

		if valTag.tagVals[0][0] == omitempty {

			if !HasValue(v, topStruct, currentStruct, current, typ, kind, blank) {
				return
			}
			continue
		}

		if v.validateField(topStruct, currentStruct, current, typ, kind, errPrefix, errs, valTag, name, customName) {
			return
		}
	}

	if dive {
		// traverse slice or map here
		// or panic ;)
		switch kind {
		case reflect.Slice, reflect.Array:
			v.traverseSlice(topStruct, currentStruct, current, errPrefix, errs, diveSubTag, name, customName, partial, exclude, includeExclude)
		case reflect.Map:
			v.traverseMap(topStruct, currentStruct, current, errPrefix, errs, diveSubTag, name, customName, partial, exclude, includeExclude)
		default:
			// throw error, if not a slice or map then should not have gotten here
			// bad dive tag
			panic("dive error! can't dive on a non slice or map")
		}
	}
}

// traverseSlice traverses a Slice or Array's elements and passes them to traverseField for validation
func (v *Validate) traverseSlice(topStruct reflect.Value, currentStruct reflect.Value, current reflect.Value, errPrefix string, errs ValidationErrors, tag, name, customName string, partial bool, exclude bool, includeExclude map[string]*struct{}) {

	for i := 0; i < current.Len(); i++ {
		v.traverseField(topStruct, currentStruct, current.Index(i), errPrefix, errs, false, tag, fmt.Sprintf(arrayIndexFieldName, name, i), fmt.Sprintf(arrayIndexFieldName, customName, i), partial, exclude, includeExclude)
	}
}

// traverseMap traverses a map's elements and passes them to traverseField for validation
func (v *Validate) traverseMap(topStruct reflect.Value, currentStruct reflect.Value, current reflect.Value, errPrefix string, errs ValidationErrors, tag, name, customName string, partial bool, exclude bool, includeExclude map[string]*struct{}) {

	for _, key := range current.MapKeys() {
		v.traverseField(topStruct, currentStruct, current.MapIndex(key), errPrefix, errs, false, tag, fmt.Sprintf(mapIndexFieldName, name, key.Interface()), fmt.Sprintf(mapIndexFieldName, customName, key.Interface()), partial, exclude, includeExclude)
	}
}

// validateField validates a field based on the provided tag's key and param values and returns true if there is an error or false if all ok
func (v *Validate) validateField(topStruct reflect.Value, currentStruct reflect.Value, current reflect.Value, currentType reflect.Type, currentKind reflect.Kind, errPrefix string, errs ValidationErrors, valTag *tagVals, name, customName string) bool {

	var valFunc Func
	var ok bool

	if valTag.isOrVal {

		errTag := blank

		for _, val := range valTag.tagVals {

			valFunc, ok = v.validationFuncs[val[0]]
			if !ok {
				panic(strings.TrimSpace(fmt.Sprintf(undefinedValidation, name)))
			}

			if valFunc(v, topStruct, currentStruct, current, currentType, currentKind, val[1]) {
				return false
			}

			errTag += orSeparator + val[0]
		}

		if valTag.isAlias {
			errs[errPrefix+name] = &FieldError{
				Name:      customName,
				Field:     name,
				Tag:       valTag.tag,
				ActualTag: errTag[1:],
				Value:     current.Interface(),
				Type:      currentType,
				Kind:      currentKind,
			}
		} else {
			errs[errPrefix+name] = &FieldError{
				Name:      customName,
				Field:     name,
				Tag:       errTag[1:],
				ActualTag: errTag[1:],
				Value:     current.Interface(),
				Type:      currentType,
				Kind:      currentKind,
			}
		}

		return true
	}

	valFunc, ok = v.validationFuncs[valTag.tagVals[0][0]]
	if !ok {
		panic(strings.TrimSpace(fmt.Sprintf(undefinedValidation, name)))
	}

	if valFunc(v, topStruct, currentStruct, current, currentType, currentKind, valTag.tagVals[0][1]) {
		return false
	}

	errs[errPrefix+name] = &FieldError{
		Name:      customName,
		Field:     name,
		Tag:       valTag.tag,
		ActualTag: valTag.tagVals[0][0],
		Value:     current.Interface(),
		Param:     valTag.tagVals[0][1],
		Type:      currentType,
		Kind:      currentKind,
	}

	return true
}
