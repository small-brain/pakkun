/*
    parse.go

    A module for parsing source code and extracting desired functions.
    
    Author: Justin Chen
    2.14.2017

    Boston University 
    Computer Science

    Dependencies:        exuberant ctags, and mongodb driver for go (http://labix.org/mgo)
    Operating systems:   GNU Linux, OS X
    Supported languages: C, C++, C#, Erlang, Lisp, Lua, Java, Javascript, and Python
*/

package parse

import (
	"strings"
    "os/exec"
    "bufio"
    "sync"
    "os"
    "io/ioutil"
    "log"
    "fmt"
    "hash/fnv"
)

/*
    Name  - File name
    Path  - Full path to file
    Funcs - List of functions that match desired types
*/
type File struct {
    Id    uint32 `json:"id" bson:"_id,omitempty"`
    Name  string
    Path  string
    Funcs []Function
}

/*
    Return a list containing function source code
*/
func (f *File) GetFuncs() []string {
    fn := []string{}
    for _, f := range f.Funcs {
        fn = append(fn, f.Name)
    }
    return fn
}

/*
    Id     - Relative position in the file. Ctags returns the function headers in order
             Will need this order later when splitting the file to extract the function source.
    Name   - Function name
    InType - Array of input types
    Output - Array of output types
*/
type Function struct {
    Id      uint32
    Name    string
    Header  string
    InType  []string
    OutType []string
    Source  string
}


func getLangExt(lang string) string {
    langMap := map[string]string {"c":"c", "c++":"cpp", "cpp":"cpp", "c#":"cs",
                                  "cs":"cs", "erlang":"erl", "java":"java",
                                  "javascript":"js", "lisp":"lsp", "lua":"lua", "python":"py"}
    return langMap[strings.TrimSpace(lang)]
}

func getFuncTerm(ext string) string {
    extMap := map[string]string {"c":"function", "cpp":"function", "cs":"method",
                                 "erl":"function", "java":"method", "js":"function",
                                 "lsp":"function", "lua":"function", "py":"function"}
    return extMap[ext]
}


/*
    Caller should always check the ok variable returned. The first three returns values are not always
    guaranteed to return the correct values.
*/
func parseJavaFuncHeader(header string, funcTypes map[string]bool) (string, []string, []string, bool) {
    // Ignore single-line comments on function header line and remove trailing spaces
    header = strings.TrimSpace(strings.Split(header, "//")[0])

    // 59 is byte value of ; meaning header is from abstract class and not an actual function header
    if header[len(header)-1] == 59 {
        return header, []string{}, []string{}, false
    }

    // Left part contains visibility modifier, return type (can be composed of multiple keywords),
    //      and function name
    // Right part contains input types
	split := strings.Split(header, "(")
    fname := ""
    in    := []string{}
    out   := []string{}
    ok    := false
    nonparameters := []string{}

	if len(split) == 2 {
        var wg sync.WaitGroup
        halt := false

	    // Check return type
        // If the header is a class header, it will only have a public modifier and the clas name
        // Functions have at least three keywords before the parentheses
        nonparameters = strings.Split(split[0], " ")
        fname         = nonparameters[len(nonparameters)-1]
        nonparameters = nonparameters[:len(nonparameters)-1]

	    if len(nonparameters) > 2 {
	        for _, t := range nonparameters {
                // If any types are not valid, not in the map, then stop
                // All return values must be valid
                wg.Add(1)
                go func(t string, halt *bool) {
                    defer wg.Done()
                    t = strings.TrimSpace(t)
    		        if desired, valid := funcTypes[t]; valid && desired {
                        out = append(out, t)
                    } else if !valid {
                        // fmt.Println("Non: ",t)
                        *halt = true
                    }
                }(t, &halt)
		    }
	    }

        // Check the input parameters
        parameters := strings.Split(strings.Split(split[1], ")")[0], " ")

        // Check that all the input types are valid
        // Can ignore the variables names
        for i, t := range parameters {
            if i %2 == 0 {
                wg.Add(1)
                go func(i int, t string, halt *bool) {
                    defer wg.Done()

                    // Remove the comma from the type
                    t = strings.TrimSpace(strings.Split(t, ",")[0])
                    parameters[i] = t

                    // Save input types if valid (key exists) and desired (key/value = true)
                    if desired, valid := funcTypes[t]; valid && desired {
                        in = append(in, t)
                    } else if !valid {
                        *halt = true
                    }

                }(i, t, &halt)
            }
        }

        wg.Wait()

        // If encountered an invalid type in the input or output types, or this is not a function header
        if halt || len(nonparameters) <= 2 {
            return "", in, out, ok
        }

        return fname, in, out, true
	} 

	return fname, in, out, ok
}

func hash(s string) uint32 {
        h := fnv.New32a()
        h.Write([]byte(s))
        return h.Sum32()
}

/*
    Returns a File struct containing all file and function information 
    and bool indicating if extracting the headers is complete
*/
func ParseFile(path string, funcTypes map[string]bool) (File, bool) {
    splits := strings.Split(path, "/")
    fname  := splits[len(splits)-1]

    // Use ctags to grab function headers and pipe to buff
    ctags := exec.Command("ctags", "-x", "--c-types=f", path)
    grep  := exec.Command("grep", getFuncTerm(fname))
    awk   := exec.Command("awk", "{$1=$2=$3=$4=\"\"; print $0}")
    grep.Stdin, _ = ctags.StdoutPipe()
    awk.Stdin, _  = grep.StdoutPipe()
    awkOut, _    := awk.StdoutPipe()
    buff := bufio.NewScanner(awkOut)

    _ = grep.Start()
    _ = awk.Start()
    _ = ctags.Run()
    _ = grep.Wait()
    defer awk.Wait()

    // Collect all function headers in file
    var ctagHeaders []string
    var funcHeaders []Function

    for buff.Scan() {    
        ctagHeaders = append(ctagHeaders, buff.Text()+"\n")
    }

    var wg sync.WaitGroup

    for _, header := range ctagHeaders {
        wg.Add(1)
        go func(header string) {
            defer wg.Done()
            fname, in, out, ok := parseJavaFuncHeader(header, funcTypes) 
            if ok && len(in) > 0 && len(out) > 0 {
                fn := Function{hash(fname+strings.TrimSpace(header)), fname, strings.TrimSpace(strings.Replace(header, "{", "", -1)), in, out, ""}
                funcHeaders = append(funcHeaders, fn)
            }
        }(header)
    }

    wg.Wait()

    var file File

    if len(funcHeaders) > 0 {
        file = File{hash(path), fname, path, funcHeaders}
        extractFuncSrc(&file)
    } else {
        return file, false
    }

    return file, true
}

/*
    Given a list of functions and the file path, extract function source code.
*/
func extractFuncSrc(f *File) {
    if _, err := os.Stat(f.Path); !os.IsNotExist(err) {
        var content []byte
        content, _ = ioutil.ReadFile(f.Path)
        contentStr := string(content)

        // Convert each header to a byte array and find the offset in the source code byte array
        // and extract the function
        funcLen := len(f.Funcs)
        fi      := 0
        
        for fi < funcLen {
            fn     := f.Funcs[fi]
            header := []byte(fn.Header)

            // Should never be true
            if len(header) == 0 {
                fmt.Println(header)
                log.Fatal()
            }

            src := balance(content, strings.Index(contentStr, fn.Header))
            if len(src) > 0 {
                f.Funcs[fi].Source = src
            } else {
                // If function's curly braces are unbalanced, delete this entry
                f.Funcs = append(f.Funcs[:fi], f.Funcs[fi+1:]...)
            }
            fi++
        }
    }
}

func insert(slice []byte, index int, item byte) []byte {
    slice = slice[0:len(slice)+1]
    copy(slice[index+1:], slice[index:])
    slice[index] = item
    return slice
}

/*
    Balance the curly braces
    arr - byte array of file
    m - index of 
*/
func balance(arr []byte, m int) string {
    start := m
    count := 0

    // Find index of first left curly brace { = 123 (byte value)
    for {
        if m < len(arr) {
            if arr[m] == 123 {
                count++
                m++
                break
            }
        } else {
            c   := []byte(fmt.Sprintf("error: m:%d, len(arr): %d%s\n", m, len(arr), string(arr)))
            err := ioutil.WriteFile("/tmp/dat1", c, 0644)
            if err != nil {
                fmt.Println("error writing log to /tmp/dat1")
            }
            return ""
        }

        m++
    }

    // Match left and right curly braces
    // count should equal zero when it reaches the end of the function.
    for {
        if m >= len(arr) {
            break
        }

        if arr[m] == 123 {
            count++
        }

        if arr[m] == 125 {
            count--
        }

        if count == 0 {
            break
        }
        m++
    }

    // If curly braces are unbalanced, return an empty string
    // Cannot naively append or insert curly braces because most likely would not be syntactically correct.
    if count != 0 {
        return ""
    }

    // Ignore the left half (original) part of the slice and return the new string without newlines and tabs
    return strings.Replace(strings.Replace(string(arr[start:m+1]), "\n", "", -1), "\t", "", -1)
}