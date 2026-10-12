package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var uuidPattern = regexp.MustCompile(`(?i)^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func identifier(value string) error {
	if !uuidPattern.MatchString(value) {
		return errors.New("expected a UUID identifier")
	}
	return nil
}
func optionalID(value string) (*string, error) {
	if value == "" {
		return nil, nil
	}
	if err := identifier(value); err != nil {
		return nil, err
	}
	return &value, nil
}

type localConfig struct {
	APIURL    string `json:"apiUrl"`
	ProjectID string `json:"projectId"`
}

func readLocalConfig() (localConfig, error) {
	dir, err := os.Getwd()
	if err != nil {
		return localConfig{}, err
	}
	for {
		b, err := os.ReadFile(filepath.Join(dir, ".nnun.json"))
		if err == nil {
			var cfg localConfig
			if err := json.Unmarshal(b, &cfg); err != nil {
				return cfg, errors.New("invalid .nnun.json")
			}
			return cfg, nil
		}
		if !os.IsNotExist(err) {
			return localConfig{}, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return localConfig{}, errors.New("no project selected; run nnun use <project-id>, or pass --project <project-id>")
}

func selectedProject(explicit, origin string) (string, error) {
	if explicit != "" {
		return explicit, identifier(explicit)
	}
	cfg, err := readLocalConfig()
	if err != nil {
		return "", err
	}
	if cfg.APIURL != origin {
		return "", errors.New("selected project belongs to another API URL; pass its --api-url explicitly")
	}
	return cfg.ProjectID, identifier(cfg.ProjectID)
}

func writeLocalConfig(cfg localConfig) error {
	path := ".nnun.json"
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("refusing to replace a symlink at .nnun.json")
	}
	b, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(".", ".nnun-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	// Windows cannot rename over an existing file. The config contains no secrets.
	if err := os.Rename(tmp.Name(), path); err != nil {
		return os.WriteFile(path, append(b, '\n'), 0600)
	}
	return nil
}

func printJSON(out io.Writer, value any) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func newCommand() *cobra.Command {
	var apiURL, projectID string
	var jsonOutput bool
	c := &client{http: secureHTTP(), store: systemStore{}}
	root := &cobra.Command{Use: "nnun", Short: "Manage Nnun projects and upload original TeX/PDF files", Version: version, SilenceUsage: true, SilenceErrors: true}
	root.PersistentFlags().StringVar(&apiURL, "api-url", envDefault("NNUN_API_URL", defaultAPI), "Nnun website origin")
	root.PersistentFlags().StringVar(&projectID, "project", "", "Project UUID (overrides .nnun.json)")
	root.PersistentFlags().BoolVar(&jsonOutput, "json", false, "Print machine-readable JSON")
	root.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		origin, err := validateOrigin(apiURL)
		if err != nil {
			return err
		}
		c.origin = origin
		c.tokenOverride = os.Getenv("NNUN_TOKEN")
		return nil
	}

	var noBrowser bool
	login := &cobra.Command{Use: "login", Short: "Sign in through your browser", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return c.login(cmd.Context(), cmd.OutOrStdout(), noBrowser)
	}}
	login.Flags().BoolVar(&noBrowser, "no-browser", false, "Print the login URL without opening a browser")
	root.AddCommand(login)
	root.AddCommand(&cobra.Command{Use: "logout", Short: "Remove this website's locally stored CLI credentials", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if c.tokenOverride != "" {
			return errors.New("NNUN_TOKEN is supplied by your environment; unset it to stop using it")
		}
		if err := c.store.Delete(c.origin); err != nil {
			return errors.New("could not delete credentials from OS credential store")
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Local CLI credentials removed.")
		return nil
	}})
	root.AddCommand(&cobra.Command{Use: "whoami", Short: "Show the authenticated account", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var user struct {
			ID    string `json:"id"`
			Email string `json:"email"`
		}
		if err := c.whoami(cmd.Context(), &user); err != nil {
			return err
		}
		if jsonOutput {
			return printJSON(cmd.OutOrStdout(), user)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", user.ID, user.Email)
		return nil
	}})

	projects := &cobra.Command{Use: "projects", Short: "List, create, and inspect projects"}
	projects.AddCommand(&cobra.Command{Use: "list", Short: "List your projects", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var response struct {
			Projects []project `json:"projects"`
		}
		if err := c.api(cmd.Context(), "GET", "", nil, &response); err != nil {
			return err
		}
		if jsonOutput {
			return printJSON(cmd.OutOrStdout(), response)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tVISIBILITY")
		for _, p := range response.Projects {
			fmt.Fprintf(w, "%s\t%s\t%s\n", p.ID, p.Name, p.Visibility)
		}
		return w.Flush()
	}})
	var visibility string
	create := &cobra.Command{Use: "create <name>", Short: "Create a project (private by default)", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if visibility != "private" && visibility != "public" {
			return errors.New("visibility must be private or public")
		}
		var response struct {
			Project project `json:"project"`
		}
		if err := c.api(cmd.Context(), "POST", "", map[string]any{"name": args[0], "visibility": visibility}, &response); err != nil {
			return err
		}
		if jsonOutput {
			return printJSON(cmd.OutOrStdout(), response)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Created %s (%s). Select it with: nnun use %s\n", response.Project.Name, response.Project.Visibility, response.Project.ID)
		return nil
	}}
	create.Flags().StringVar(&visibility, "visibility", "private", "private or public (anyone with the link can read original files)")
	projects.AddCommand(create)
	projects.AddCommand(&cobra.Command{Use: "show [project-id]", Short: "Show a project's folders and files", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id := projectID
		if len(args) > 0 {
			id = args[0]
		}
		id, err := selectedProject(id, c.origin)
		if err != nil {
			return err
		}
		var tree projectTree
		if err := c.api(cmd.Context(), "GET", "/"+id, nil, &tree); err != nil {
			return err
		}
		return printJSON(cmd.OutOrStdout(), tree)
	}})
	root.AddCommand(projects)
	root.AddCommand(&cobra.Command{Use: "use <project-id-or-name>", Short: "Select a project in this directory's .nnun.json", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id := args[0]
		if !uuidPattern.MatchString(id) {
			var response struct {
				Projects []project `json:"projects"`
			}
			if err := c.api(cmd.Context(), "GET", "", nil, &response); err != nil {
				return err
			}
			id = ""
			for _, p := range response.Projects {
				if p.Name == args[0] {
					if id != "" {
						return errors.New("multiple projects have that name; select one by its UUID")
					}
					id = p.ID
				}
			}
			if id == "" {
				return errors.New("project name not found; run nnun projects list")
			}
		}
		if err := identifier(id); err != nil {
			return err
		}
		var tree projectTree
		if err := c.api(cmd.Context(), "GET", "/"+id, nil, &tree); err != nil {
			return err
		}
		if err := writeLocalConfig(localConfig{c.origin, id}); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Selected %s. Saved .nnun.json (no credentials).\n", tree.Project.Name)
		return nil
	}})

	folders := &cobra.Command{Use: "folders", Short: "Manage project folders"}
	var parentID string
	folderCreate := &cobra.Command{Use: "create <name>", Short: "Create a folder", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := selectedProject(projectID, c.origin)
		if err != nil {
			return err
		}
		parent, err := optionalID(parentID)
		if err != nil {
			return err
		}
		var response struct {
			Folder folder `json:"folder"`
		}
		if err := c.api(cmd.Context(), "POST", "/"+id+"/folders", map[string]any{"name": args[0], "parentId": parent}, &response); err != nil {
			return err
		}
		if jsonOutput {
			return printJSON(cmd.OutOrStdout(), response)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\n", response.Folder.ID, response.Folder.Name)
		return nil
	}}
	folderCreate.Flags().StringVar(&parentID, "parent", "", "Parent folder UUID (default: project root)")
	folders.AddCommand(folderCreate)
	root.AddCommand(folders)

	files := &cobra.Command{Use: "files", Short: "List, download, or remove originals"}
	files.AddCommand(&cobra.Command{Use: "complete <file-id>", Short: "Retry verification after an interrupted upload confirmation", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := selectedProject(projectID, c.origin)
		if err != nil {
			return err
		}
		if err := identifier(args[0]); err != nil {
			return err
		}
		var response struct {
			File projectFile `json:"file"`
		}
		if err := c.api(cmd.Context(), "POST", "/"+id+"/files/"+args[0]+"/complete", nil, &response); err != nil {
			return err
		}
		if jsonOutput {
			return printJSON(cmd.OutOrStdout(), response)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Verified %s (%s)\n", response.File.Name, response.File.ID)
		return nil
	}})
	files.AddCommand(&cobra.Command{Use: "list", Short: "List all completed files in the project", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		id, err := selectedProject(projectID, c.origin)
		if err != nil {
			return err
		}
		var tree projectTree
		if err := c.api(cmd.Context(), "GET", "/"+id, nil, &tree); err != nil {
			return err
		}
		if jsonOutput {
			return printJSON(cmd.OutOrStdout(), tree.Files)
		}
		w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tBYTES\tFOLDER")
		for _, f := range tree.Files {
			parent := "/"
			if f.FolderID != nil {
				parent = *f.FolderID
			}
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", f.ID, f.Name, f.SizeBytes, parent)
		}
		return w.Flush()
	}})
	var output string
	download := &cobra.Command{Use: "download <file-id>", Short: "Download an original without overwriting a local file", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := selectedProject(projectID, c.origin)
		if err != nil {
			return err
		}
		if err := identifier(args[0]); err != nil {
			return err
		}
		target := output
		if target == "" {
			var tree projectTree
			if err := c.api(cmd.Context(), "GET", "/"+id, nil, &tree); err != nil {
				return err
			}
			for _, f := range tree.Files {
				if f.ID == args[0] {
					target = filepath.Base(f.Name)
					break
				}
			}
			if target == "" || target == "." || target == ".." {
				return errors.New("file not found; supply --output to specify its local destination")
			}
		}
		if err := c.download(cmd.Context(), id, args[0], target); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Downloaded", target)
		return nil
	}}
	download.Flags().StringVarP(&output, "output", "o", "", "Destination path (must not exist)")
	files.AddCommand(download)
	var yes bool
	remove := &cobra.Command{Use: "remove <file-id>", Aliases: []string{"delete"}, Short: "Permanently remove an original file", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := selectedProject(projectID, c.origin)
		if err != nil {
			return err
		}
		if err := identifier(args[0]); err != nil {
			return err
		}
		if !yes {
			fmt.Fprintf(cmd.ErrOrStderr(), "Permanently remove file %s? Type its full ID to confirm: ", args[0])
			var answer string
			if _, err := fmt.Fscanln(cmd.InOrStdin(), &answer); err != nil || answer != args[0] {
				return errors.New("removal cancelled; use --yes for noninteractive confirmation")
			}
		}
		if err := c.api(cmd.Context(), "DELETE", "/"+id+"/files/"+args[0], nil, nil); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Removed", args[0])
		return nil
	}}
	remove.Flags().BoolVarP(&yes, "yes", "y", false, "Confirm permanent removal")
	files.AddCommand(remove)
	root.AddCommand(files)
	var folderID string
	push := &cobra.Command{Use: "push <file> [file...]", Short: "Upload immutable .tex, .txt, .md, or .pdf originals", Args: cobra.MinimumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		id, err := selectedProject(projectID, c.origin)
		if err != nil {
			return err
		}
		folder, err := optionalID(folderID)
		if err != nil {
			return err
		}
		// Validate the whole batch before reserving any upload.
		for _, path := range args {
			if _, err := readUpload(path); err != nil {
				return err
			}
		}
		var uploaded []projectFile
		for _, path := range args {
			if !jsonOutput {
				fmt.Fprintln(cmd.ErrOrStderr(), "Uploading", filepath.Base(path))
			}
			f, err := c.push(cmd.Context(), id, folder, path)
			if err != nil {
				return fmt.Errorf("%s: %w", filepath.Base(path), err)
			}
			uploaded = append(uploaded, f)
			if !jsonOutput {
				fmt.Fprintf(cmd.OutOrStdout(), "Uploaded %s (%s)\n", f.Name, f.ID)
			}
		}
		if jsonOutput {
			return printJSON(cmd.OutOrStdout(), uploaded)
		}
		fmt.Fprintln(cmd.OutOrStdout(), c.origin+"/projects/"+id)
		return nil
	}}
	push.Flags().StringVar(&folderID, "folder", "", "Destination folder UUID (default: project root)")
	root.AddCommand(push)
	return root
}

func envDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
