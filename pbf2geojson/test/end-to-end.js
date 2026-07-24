
/**
  End-to-end tests of a small pbf extract.

  The somes.osm.pbf extract will be automatically downloaded before testing.
  @see: ./pretest.sh for more details, or run manually to download file.
**/

var path = require('path'),
    through = require('through2'),
    pbf2json = require('../index');

function test( name, tags, cb ){
  var pbfPath = path.resolve(__dirname) + '/vancouver_canada.osm.pbf',
      count = 0;

  pbf2json.createReadStream({ file: pbfPath, tags: tags })
    .pipe( through.obj( function( obj, _, next ){
      try {
        validateFeature(obj);
        if(!matchesTagConditions(obj.properties.tags, tags)){
          throw new Error('Feature ' + obj.id + ' does not match requested tags ' + tags.join(','));
        }
        count++;
        next();
      } catch (err) {
        next(err);
      }
    }))
    .on('error', function(err){
      console.error('end-to-end test failed:', name, err.message);
      process.exit(1);
    })
    .on('finish', function assert(){
      if(count === 0){
        console.error('end-to-end test returned no Features:', name);
        process.exit(1);
      }
      cb();
    });
}

var tests = [
  [ 'single',     ['building'] ],
  [ 'multiple',   ['building','shop'] ],
  [ 'colon',      ['addr:housenumber'] ],
  [ 'group',      ['addr:housenumber+addr:street'] ],
  [ 'multigroup', ['highway+name','waterway+name'] ],
  [ 'value',      ['amenity~toilets'] ],
  [ 'multivalue', ['amenity~toilets','amenity~kindergarten'] ]
];

function next(){
  var t = tests.shift();
  if( t ){ test( t[0], t[1], next ); }
}

var validateFeature = function(feature) {
  if(!feature || feature.type !== 'Feature'){ throw new Error('expected a GeoJSON Feature'); }
  if(!feature.geometry || !feature.geometry.type){ throw new Error('Feature is missing geometry'); }
  if(!feature.properties || !feature.properties.osm_type){ throw new Error('Feature is missing properties.osm_type'); }
  if(!Number.isInteger(feature.properties.osm_id)){ throw new Error('Feature is missing an integer properties.osm_id'); }
};

var matchesTagConditions = function(actualTags, requestedGroups) {
  return requestedGroups.some(function(group) {
    return group.split('+').every(function(condition) {
      var keyAndValue = condition.split('~'),
          key = keyAndValue[0];
      if(!Object.prototype.hasOwnProperty.call(actualTags, key)){ return false; }
      return keyAndValue.length === 1 || actualTags[key] === keyAndValue[1];
    });
  });
};

// run each test synchronously
next();
