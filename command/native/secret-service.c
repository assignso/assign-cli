/* Private libsecret transport. No shell, prompts, native messages or token arguments. */
#include <libsecret/secret.h>
#include <gio/gio.h>
#include <stdio.h>
#include <string.h>

static const SecretSchema schema = {
  .name = "Assign CLI", .flags = SECRET_SCHEMA_DONT_MATCH_NAME,
  .attributes = {{ "service", SECRET_SCHEMA_ATTRIBUTE_STRING },
                 { "username", SECRET_SCHEMA_ATTRIBUTE_STRING }, { NULL, 0 }}
};

static int failure(GError *error) {
  if (!error) return 13;
  int code = 13;
  if (g_error_matches(error, SECRET_ERROR, SECRET_ERROR_IS_LOCKED)) code = 11;
  if (g_error_matches(error, G_DBUS_ERROR, G_DBUS_ERROR_SERVICE_UNKNOWN) ||
      g_error_matches(error, G_DBUS_ERROR, G_DBUS_ERROR_NAME_HAS_NO_OWNER) ||
      g_error_matches(error, G_IO_ERROR, G_IO_ERROR_NOT_FOUND) ||
      g_error_matches(error, G_IO_ERROR, G_IO_ERROR_CONNECTION_REFUSED)) code = 10;
  if (g_error_matches(error, G_DBUS_ERROR, G_DBUS_ERROR_ACCESS_DENIED) ||
      g_error_matches(error, G_IO_ERROR, G_IO_ERROR_PERMISSION_DENIED)) code = 12;
  g_error_free(error);
  return code;
}

/* Each field is NUL-delimited and bounded; stdin is the only credential input. */
static char *field(void) {
  GString *value = g_string_new(NULL);
  for (size_t n = 0; n <= 1024 * 1024; n++) {
    int c = getchar();
    if (c == 0) return g_string_free(value, FALSE);
    if (c == EOF) break;
    g_string_append_c(value, c);
  }
  g_string_free(value, TRUE);
  return NULL;
}

int main(int argc, char **argv) {
  if (argc != 2 || (strcmp(argv[1], "read") && strcmp(argv[1], "write") && strcmp(argv[1], "delete"))) return 13;
  char *service = field(), *username = field(), *payload = field();
  if (!service || !username || !payload || strcmp(service, "Assign CLI") || getchar() != EOF) return 13;
  GError *error = NULL;
  GDBusConnection *bus = g_bus_get_sync(G_BUS_TYPE_SESSION, NULL, &error);
  if (!bus) { int code = failure(error); return code == 12 ? 12 : 10; }
  g_object_unref(bus);
  SecretService *store = secret_service_get_sync(SECRET_SERVICE_OPEN_SESSION, NULL, &error);
  if (!store) return failure(error);
  GHashTable *attributes = secret_attributes_build(&schema, "service", service, "username", username, NULL);
  /* No UNLOCK or prompt flags: locked records fail instead of invoking a UI. */
  GList *items = secret_service_search_sync(store, &schema, attributes, SECRET_SEARCH_ALL, NULL, &error);
  int result = 0;
  if (error) { result = failure(error); goto done; }
  for (GList *node = items; node; node = node->next) {
    if (secret_item_get_locked(SECRET_ITEM(node->data))) { result = 11; goto done; }
  }
  if (!strcmp(argv[1], "read")) {
    if (!items) { result = 4; goto done; }
    SecretItem *item = SECRET_ITEM(items->data);
    if (!secret_item_load_secret_sync(item, NULL, &error)) { result = failure(error); goto done; }
    SecretValue *value = secret_item_get_secret(item);
    if (!value) { result = 13; goto done; }
    gsize length = 0;
    const char *text = secret_value_get(value, &length);
    if (fwrite(text, 1, length, stdout) != length || fflush(stdout)) result = 13;
    secret_value_unref(value);
  } else if (!strcmp(argv[1], "delete")) {
    for (GList *node = items; node; node = node->next) {
      if (!secret_item_delete_sync(SECRET_ITEM(node->data), NULL, &error)) { result = failure(error); break; }
    }
  } else {
    SecretCollection *collection = secret_collection_for_alias_sync(store, SECRET_COLLECTION_DEFAULT, SECRET_COLLECTION_NONE, NULL, &error);
    if (!collection) { result = failure(error); goto done; }
    if (secret_collection_get_locked(collection)) { result = 11; g_object_unref(collection); goto done; }
    SecretValue *value = secret_value_new(payload, -1, "text/plain");
    SecretItem *item = secret_item_create_sync(collection, &schema, attributes, "Assign CLI", value, SECRET_ITEM_CREATE_REPLACE, NULL, &error);
    if (!item) result = failure(error);
    else g_object_unref(item);
    secret_value_unref(value);
    g_object_unref(collection);
  }
done:
  g_list_free_full(items, g_object_unref);
  g_hash_table_unref(attributes);
  g_object_unref(store);
  memset(payload, 0, strlen(payload));
  g_free(payload); g_free(service); g_free(username);
  return result;
}
