-- v29 (compatible with v9+): Clear incorrect personal filtering space values
UPDATE user_login SET space_room = NULL WHERE space_room LIKE '!management:%';
