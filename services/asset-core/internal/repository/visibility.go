package repository

import "fmt"

// visibleFolderPredicate returns the SQL rule for a folder that may be exposed
// through normal Asset Core reads or mutations. A row is visible only when the
// row itself and every ancestor in its ltree path are active.
//
// The alias is a repository-owned SQL identifier, never request input.
func visibleFolderPredicate(alias string) string {
	return fmt.Sprintf(`
		%s.deleted_at IS NULL
		AND NOT EXISTS (
			SELECT 1
			FROM folders AS deleted_ancestor
			WHERE deleted_ancestor.org_id = %s.org_id
			  AND deleted_ancestor.deleted_at IS NOT NULL
			  AND deleted_ancestor.path @> %s.path
		)`, alias, alias, alias)
}

// visibleMetadataPredicate combines item visibility with the visibility of its
// containing folder and all of that folder's ancestors.
func visibleMetadataPredicate(itemAlias, folderAlias string) string {
	return fmt.Sprintf(`
		%s.deleted_at IS NULL
		AND %s`, itemAlias, visibleFolderPredicate(folderAlias))
}
