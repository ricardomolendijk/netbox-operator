VRF_RD_MAX_LENGTH = 21
PREFIX_LENGTH_MAX = 127

# Defined again, with a different value, in dcim/constants.py. The extractor must refuse
# to guess between the two rather than pick one at random.
AMBIGUOUS_MAX_LENGTH = 64

# The set of models a Prefix may be scoped to, written as bare model names -- the serializer
# names it by symbol, and the `app_label.model` half is resolved against models.json.
PREFIX_SCOPE_TYPES = ('region', 'site')
