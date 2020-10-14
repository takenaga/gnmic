package cmd

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"strings"

	goprompt "github.com/c-bata/go-prompt"
	"github.com/c-bata/go-prompt/completer"
	homedir "github.com/mitchellh/go-homedir"
	"github.com/nsf/termbox-go"
	"github.com/openconfig/goyang/pkg/yang"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/spf13/viper"
)

var promptMode bool
var promptHistory []string
var schemaTree = &yang.Entry{
	Dir: make(map[string]*yang.Entry),
}
var colorMapping = map[string]goprompt.Color{
	"black":      goprompt.Black,
	"dark_red":   goprompt.DarkRed,
	"darkg_reen": goprompt.DarkGreen,
	"brown":      goprompt.Brown,
	"dark_blue":  goprompt.DarkBlue,
	"purple":     goprompt.Purple,
	"cyan":       goprompt.Cyan,
	"light_gray": goprompt.LightGray,
	"dark_grey":  goprompt.DarkGray,
	"red":        goprompt.Red,
	"green":      goprompt.Green,
	"yellow":     goprompt.Yellow,
	"blue":       goprompt.Blue,
	"fuchsia":    goprompt.Fuchsia,
	"turquoise":  goprompt.Turquoise,
	"white":      goprompt.White,
}

func getColor(flagName string) goprompt.Color {
	if cgoprompt, ok := colorMapping[viper.GetString(flagName)]; ok {
		return cgoprompt
	}
	defColor := "yellow"
	promptModeCmd.Flags().VisitAll(
		func(f *pflag.Flag) {
			if f.Name == strings.Replace(flagName, "prompt-", "", 1) {
				defColor = f.DefValue
				return
			}
		},
	)
	return colorMapping[defColor]
}

var promptModeCmd = &cobra.Command{
	Use:   "prompt",
	Short: "enter the interactive gnmic prompt mode",
	RunE: func(cmd *cobra.Command, args []string) error {
		err := generateYangSchema(promptDirs, promptFiles, promptExcluded)
		if err != nil {
			if !viper.GetBool("log") {
				fmt.Fprintf(os.Stderr, "ERR: failed to load paths from yang: %v\n", err)
			}
		}
		promptMode = true
		// load history
		promptHistory = make([]string, 0, 256)
		home, err := homedir.Dir()
		if err != nil {
			if viper.GetBool("debug") {
				log.Printf("failed to get home directory: %v", err)
			}
			return nil
		}
		content, err := ioutil.ReadFile(home + "/.gnmic.history")
		if err != nil {
			if viper.GetBool("debug") {
				log.Printf("failed to read history file: %v", err)
			}
			return nil
		}
		history := strings.Split(string(content), "\n")
		for i := range history {
			if history[i] != "" {
				promptHistory = append(promptHistory, history[i])
			}
		}
		return nil
	},
	PostRun: func(cmd *cobra.Command, args []string) {
		cmd.ResetFlags()
		//initPromptFlags(cmd)
	},
	SilenceUsage: true,
}

var promptQuitCmd = &cobra.Command{
	Use:   "quit",
	Short: "quit the gnmic-prompt",
	Run: func(cmd *cobra.Command, args []string) {
		// save history
		home, err := homedir.Dir()
		if err != nil {
			os.Exit(0)
		}
		f, err := os.Create(home + "/.gnmic.history")
		if err != nil {
			os.Exit(0)
		}
		l := len(promptHistory)
		if l > 128 {
			promptHistory = promptHistory[l-128:]
		}
		for i := range promptHistory {
			f.WriteString(promptHistory[i] + "\n")
		}
		f.Close()
		os.Exit(0)
	},
}

func init() {
	rootCmd.AddCommand(promptModeCmd)
	initPromptFlags(promptModeCmd)
}

var promptFiles []string
var promptExcluded []string
var promptDirs []string

// used to init or reset pathCmd flags for gnmic-prompt mode
func initPromptFlags(cmd *cobra.Command) {
	cmd.Flags().StringArrayVarP(&promptFiles, "file", "", []string{}, "yang files to get the paths")
	cmd.Flags().StringArrayVarP(&promptExcluded, "exclude", "", []string{}, "yang modules to be excluded from path generation")
	cmd.Flags().StringArrayVarP(&promptDirs, "dir", "", []string{}, "directories to search yang includes and imports")
	cmd.Flags().Uint16("max-suggestions", 5, "terminal suggestion max list size")
	cmd.Flags().String("prefix-color", "yellow", "terminal prefix color")
	cmd.Flags().String("suggestions-bg-color", "black", "suggestion box background color")
	cmd.Flags().String("description-bg-color", "yellow", "description box background color")
	viper.BindPFlag("prompt-file", cmd.LocalFlags().Lookup("file"))
	viper.BindPFlag("prompt-exclude", cmd.LocalFlags().Lookup("exclude"))
	viper.BindPFlag("prompt-dir", cmd.LocalFlags().Lookup("dir"))
	viper.BindPFlag("prompt-max-suggestions", cmd.LocalFlags().Lookup("max-suggestions"))
	viper.BindPFlag("prompt-prefix-color", cmd.LocalFlags().Lookup("prefix-color"))
	viper.BindPFlag("prompt-suggestions-bg-color", cmd.LocalFlags().Lookup("suggestions-bg-color"))
	viper.BindPFlag("prompt-description-bg-color", cmd.LocalFlags().Lookup("description-bg-color"))
}

func findMatchedXPATH(entry *yang.Entry, word string, cursor int) []goprompt.Suggest {
	suggestions := make([]goprompt.Suggest, 0, 4)
	cword := word[cursor:]
	for name, child := range entry.Dir {
		pathelem := "/" + name
		if strings.HasPrefix(pathelem, cword) {
			node := ""
			if os.PathSeparator != '/' {
				node = fmt.Sprintf("%s%s", word[:cursor], pathelem)
				suggestions = append(suggestions, goprompt.Suggest{Text: node, Description: child.Description})
			} else {
				if len(cword) >= 1 && cword[0] == '/' {
					node = name
				} else {
					node = pathelem
				}
				suggestions = append(suggestions, goprompt.Suggest{Text: node, Description: child.Description})
			}
			if child.Key != "" { // list
				keylist := strings.Split(child.Key, " ")
				for _, key := range keylist {
					node = fmt.Sprintf("%s[%s=*]", node, key)
				}
				suggestions = append(suggestions, goprompt.Suggest{Text: node, Description: child.Description})
			}
		} else if strings.HasPrefix(cword, pathelem) {
			var prevC rune
			var bracketCount int
			var endIndex int = -1
			var stop bool
			for i, c := range cword {
				switch c {
				case '[':
					bracketCount++
				case ']':
					if prevC != '\\' {
						bracketCount--
						endIndex = i
					}
				case '/':
					if i != 0 && bracketCount == 0 {
						endIndex = i
						stop = true
					}
				}
				if stop {
					break
				}
				prevC = c
			}
			if bracketCount == 0 {
				if endIndex >= 0 {
					suggestions = append(suggestions, findMatchedXPATH(child, word, cursor+endIndex)...)
				} else {
					suggestions = append(suggestions, findMatchedXPATH(child, word, cursor+len(pathelem))...)
				}
			}
		}
	}
	return suggestions
}

var filePathCompleter = completer.FilePathCompleter{
	IgnoreCase: true,
	Filter: func(fi os.FileInfo) bool {
		fmt.Println(fi.Name())
		return fi.IsDir() || !strings.HasPrefix(fi.Name(), ".")
	},
}

var yangPathCompleter = completer.FilePathCompleter{
	IgnoreCase: true,
	Filter: func(fi os.FileInfo) bool {
		fmt.Println(fi.Name())
		return fi.IsDir() || strings.HasSuffix(fi.Name(), ".yang")
	},
}

var dirPathCompleter = completer.FilePathCompleter{
	IgnoreCase: true,
	Filter: func(fi os.FileInfo) bool {
		return fi.IsDir()
	},
}

func findDynamicSuggestions(annotation string, doc goprompt.Document) []goprompt.Suggest {
	switch annotation {
	case "XPATH":
		word := doc.GetWordBeforeCursor()
		suggestions := make([]goprompt.Suggest, 0, 16)
		for _, entry := range schemaTree.Dir {
			suggestions = append(suggestions, findMatchedXPATH(entry, word, 0)...)
		}
		return suggestions
	case "FILE":
		return filePathCompleter.Complete(doc)
	case "YANG":
		return yangPathCompleter.Complete(doc)
	case "MODEL":
		suggestions := make([]goprompt.Suggest, 0, len(schemaTree.Dir))
		for name, dir := range schemaTree.Dir {
			if dir != nil {
				suggestions = append(suggestions, goprompt.Suggest{Text: name, Description: dir.Description})
				continue
			}
			suggestions = append(suggestions, goprompt.Suggest{Text: name})
		}
		return goprompt.FilterHasPrefix(suggestions, doc.GetWordBeforeCursor(), true)
	case "DIR":
		return dirPathCompleter.Complete(doc)
	case "ENCODING":
		suggestions := make([]goprompt.Suggest, 0, len(encodings))
		for _, sugg := range encodings {
			suggestions = append(suggestions, goprompt.Suggest{Text: sugg[0], Description: sugg[1]})
		}
		return goprompt.FilterHasPrefix(suggestions, doc.GetWordBeforeCursor(), true)
	case "FORMAT":
		suggestions := make([]goprompt.Suggest, 0, len(formats))
		for _, sugg := range formats {
			suggestions = append(suggestions, goprompt.Suggest{Text: sugg[0], Description: sugg[1]})
		}
		return goprompt.FilterHasPrefix(suggestions, doc.GetWordBeforeCursor(), true)
	case "STORE":
		suggestions := make([]goprompt.Suggest, 0, len(dataType))
		for _, sugg := range dataType {
			suggestions = append(suggestions, goprompt.Suggest{Text: sugg[0], Description: sugg[1]})
		}
		return goprompt.FilterHasPrefix(suggestions, doc.GetWordBeforeCursor(), true)
	case "SUBSC_MODE":
		suggestions := make([]goprompt.Suggest, 0, len(subscriptionModes))
		for _, sugg := range subscriptionModes {
			suggestions = append(suggestions, goprompt.Suggest{Text: sugg[0], Description: sugg[1]})
		}
		return goprompt.FilterHasPrefix(suggestions, doc.GetWordBeforeCursor(), true)
	case "STREAM_MODE":
		suggestions := make([]goprompt.Suggest, 0, len(streamSubscriptionModes))
		for _, sugg := range streamSubscriptionModes {
			suggestions = append(suggestions, goprompt.Suggest{Text: sugg[0], Description: sugg[1]})
		}
		return goprompt.FilterHasPrefix(suggestions, doc.GetWordBeforeCursor(), true)
	}
	return []goprompt.Suggest{}
}

func showCommandArguments(b *goprompt.Buffer) {
	doc := b.Document()
	showLocalFlags := false
	command := rootCmd
	args := strings.Fields(doc.CurrentLine())
	if found, _, err := command.Find(args); err == nil {
		if command != found {
			showLocalFlags = true
		}
		command = found
	}
	maxNameLen := 0
	suggestions := make([]goprompt.Suggest, 0, 32)
	if command.HasAvailableSubCommands() {
		for _, c := range command.Commands() {
			if c.Hidden {
				continue
			}
			length := len(c.Name())
			if maxNameLen < length {
				maxNameLen = length
			}
			suggestions = append(suggestions, goprompt.Suggest{Text: c.Name(), Description: c.Short})
		}
	}
	if showLocalFlags {
		addFlags := func(flag *pflag.Flag) {
			if flag.Hidden {
				return
			}
			length := len(flag.Name)
			if maxNameLen < length+2 {
				maxNameLen = length + 2
			}
			suggestions = append(suggestions, goprompt.Suggest{Text: "--" + flag.Name, Description: flag.Usage})
		}
		command.LocalFlags().VisitAll(addFlags)
	}
	suggestions = goprompt.FilterHasPrefix(suggestions, doc.GetWordBeforeCursor(), true)
	if len(suggestions) == 0 {
		return
	}
	if err := termbox.Init(); err != nil {
		logger.Fatalf("%v", err)
	}
	w, _ := termbox.Size()
	termbox.Close()
	fmt.Printf("\n")
	maxDescLen := w - maxNameLen - 6
	format := fmt.Sprintf("  %%-%ds : %%-%ds\n", maxNameLen, maxDescLen)
	for i := range suggestions {
		length := len(suggestions[i].Description)
		if length > maxDescLen {
			fmt.Printf(format, suggestions[i].Text, suggestions[i].Description[:maxDescLen])
		} else {
			fmt.Printf(format, suggestions[i].Text, suggestions[i].Description)
		}
	}
	fmt.Printf("\n")
}

// ExecutePrompt load and run gnmic-prompt mode.
func ExecutePrompt() {
	rootCmd.AddCommand(promptQuitCmd)
	rootCmd.RemoveCommand(promptModeCmd)
	shell := &cmdPrompt{
		RootCmd: rootCmd,
		GoPromptOptions: []goprompt.Option{
			goprompt.OptionTitle("gnmic-prompt"),
			goprompt.OptionPrefix("gnmic> "),
			goprompt.OptionHistory(promptHistory),
			goprompt.OptionMaxSuggestion(uint16(viper.GetUint("prompt-max-suggestions"))),
			goprompt.OptionPrefixTextColor(getColor("prompt-prefix-color")),
			// goprompt.OptionPreviewSuggestionTextColor(goprompt.Yellow),
			goprompt.OptionPreviewSuggestionBGColor(goprompt.Black),
			goprompt.OptionSuggestionTextColor(goprompt.White),
			goprompt.OptionSuggestionBGColor(getColor("prompt-suggestions-bg-color")),
			goprompt.OptionSelectedSuggestionTextColor(goprompt.Black),
			goprompt.OptionSelectedSuggestionBGColor(goprompt.White),
			// goprompt.OptionDescriptionTextColor(goprompt.White),
			goprompt.OptionDescriptionBGColor(getColor("prompt-description-bg-color")),
			goprompt.OptionSelectedDescriptionTextColor(goprompt.Black),
			goprompt.OptionSelectedDescriptionBGColor(goprompt.White),
			goprompt.OptionScrollbarBGColor(goprompt.White),
			goprompt.OptionAddASCIICodeBind(goprompt.ASCIICodeBind{
				ASCIICode: []byte{0x3f}, Fn: showCommandArguments}),
			goprompt.OptionCompletionWordSeparator(completer.FilePathCompletionSeparator),
			goprompt.OptionCompletionOnDown(),
			goprompt.OptionShowCompletionAtStart(),
		},
	}
	shell.Run()
}

// Reference: https://github.com/stromland/cobra-prompt
// cmdPrompt requires RootCmd to run
type cmdPrompt struct {
	// RootCmd is the start point, all its sub commands and flags will be available as suggestions
	RootCmd *cobra.Command

	// GoPromptOptions is for customize go-prompt
	// see https://github.com/c-bata/go-prompt/blob/master/option.go
	GoPromptOptions []goprompt.Option
}

// Run will automatically generate suggestions for all cobra commands
// and flags defined by RootCmd and execute the selected commands.
func (co cmdPrompt) Run() {
	p := goprompt.New(
		func(in string) {
			promptArgs := strings.Fields(in)
			os.Args = append([]string{os.Args[0]}, promptArgs...)
			if len(promptArgs) > 0 {
				err := co.RootCmd.Execute()
				if err == nil && in != "" {
					promptHistory = append(promptHistory, in)
				}
			}
		},
		func(d goprompt.Document) []goprompt.Suggest {
			return findSuggestions(co, d)
		},
		co.GoPromptOptions...,
	)
	p.Run()
}

func findSuggestions(co cmdPrompt, doc goprompt.Document) []goprompt.Suggest {
	showLocalFlags := false
	command := co.RootCmd
	args := strings.Fields(doc.CurrentLine())
	if found, _, err := command.Find(args); err == nil {
		if command != found {
			showLocalFlags = true
		}
		command = found
	}

	suggestions := make([]goprompt.Suggest, 0, 32)

	// check flag annotation for the dynamic suggestion
	annotation := ""
	argnum := len(args)
	wordBefore := doc.GetWordBeforeCursor()
	if wordBefore == "" {
		if argnum >= 1 {
			annotation = command.Annotations[args[argnum-1]]
		}
	} else {
		if argnum >= 2 {
			annotation = command.Annotations[args[argnum-2]]
		}
	}
	if annotation != "" {
		return append(suggestions, findDynamicSuggestions(annotation, doc)...)
	}
	// add sub commands suggestions if they exist
	if command.HasAvailableSubCommands() {
		for _, c := range command.Commands() {
			if !c.Hidden {
				suggestions = append(suggestions, goprompt.Suggest{Text: c.Name(), Description: c.Short})
			}
		}
	}
	if showLocalFlags {
		// load local flags of the command
		addFlags := func(flag *pflag.Flag) {
			if flag.Hidden {
				return
			}
			suggestions = append(suggestions, goprompt.Suggest{Text: "--" + flag.Name, Description: flag.Usage})
		}
		command.LocalFlags().VisitAll(addFlags)
		// command.InheritedFlags().VisitAll(addFlags)
	} else {

		// persistent flags are shown if run.
		addFlags := func(flag *pflag.Flag) {
			if flag.Hidden {
				return
			}
			// if strings.HasPrefix(doc.GetWordBeforeCursor(), "--") {
			// 	suggestions = append(suggestions, goprompt.Suggest{Text: "--" + flag.Name, Description: flag.Usage})
			// } else if strings.HasPrefix(doc.GetWordBeforeCursor(), "-") && flag.Shorthand != "" {
			// 	suggestions = append(suggestions, goprompt.Suggest{Text: "-" + flag.Shorthand, Description: flag.Usage})
			// }
			if strings.HasPrefix(doc.GetWordBeforeCursor(), "-") {
				suggestions = append(suggestions, goprompt.Suggest{Text: "--" + flag.Name, Description: flag.Usage})
			}
		}
		command.LocalFlags().VisitAll(addFlags)
	}
	return goprompt.FilterHasPrefix(suggestions, doc.GetWordBeforeCursor(), true)
}